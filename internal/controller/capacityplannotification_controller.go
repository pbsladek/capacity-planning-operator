package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

const (
	defaultNotificationRequeue  = 5 * time.Minute
	defaultNotificationCooldown = 30 * time.Minute
)

// CapacityPlanNotificationReconciler delivers risk digests to external channels.
//
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplannotifications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplannotifications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplannotifications/finalizers,verbs=update
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
type CapacityPlanNotificationReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	HTTPClient *http.Client
}

func (r *CapacityPlanNotificationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("capacityPlanNotification", req.NamespacedName.String())

	var notif capacityv1.CapacityPlanNotification
	if err := r.Get(ctx, req.NamespacedName, &notif); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	planName := strings.TrimSpace(notif.Spec.PlanRefName)
	if planName == "" {
		setNotificationCondition(&notif, "Ready", metav1.ConditionFalse, "InvalidSpec", "spec.planRefName is required")
		_ = r.Status().Update(ctx, &notif)
		return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
	}

	var plan capacityv1.CapacityPlan
	if err := r.Get(ctx, types.NamespacedName{Name: planName}, &plan); err != nil {
		if errors.IsNotFound(err) {
			setNotificationCondition(&notif, "Ready", metav1.ConditionFalse, "PlanNotFound", fmt.Sprintf("CapacityPlan %q not found", planName))
			_ = r.Status().Update(ctx, &notif)
			return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
		}
		return ctrl.Result{}, err
	}

	now := time.Now()
	digest := strings.TrimSpace(plan.Status.RiskDigest)
	if digest == "" {
		digest = buildRiskDigest(now, plan.Status.TopRisks)
	}
	snapshotHash := strings.TrimSpace(plan.Status.RiskSnapshotHash)
	if snapshotHash == "" {
		snapshotHash = computeRiskSnapshotHash(plan.Status.TopRisks)
	}

	cooldown := notif.Spec.Cooldown.Duration
	if cooldown <= 0 {
		cooldown = defaultNotificationCooldown
	}
	if notif.Spec.OnChangeOnly && snapshotHash != "" && snapshotHash == notif.Status.LastDigestHash {
		setNotificationCondition(&notif, "Ready", metav1.ConditionTrue, "NoChange", "Risk snapshot unchanged; notification skipped")
		if err := r.Status().Update(ctx, &notif); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
	}
	if notif.Status.LastSentTime != nil && now.Sub(notif.Status.LastSentTime.Time) < cooldown {
		setNotificationCondition(&notif, "Ready", metav1.ConditionTrue, "Cooldown", "Notification skipped due to cooldown")
		if err := r.Status().Update(ctx, &notif); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
	}

	message := r.renderNotificationMessage(&plan)
	sentChannels := make([]string, 0, 2)
	var deliveryErrs []error

	if notif.Spec.Slack.Enabled {
		sentChannels = append(sentChannels, "slack")
		if !notif.Spec.DryRun {
			if err := r.sendSlack(ctx, notif.Namespace, notif.Spec.Slack, message); err != nil {
				deliveryErrs = append(deliveryErrs, fmt.Errorf("slack: %w", err))
			}
		}
	}
	if notif.Spec.Email.Enabled {
		sentChannels = append(sentChannels, "email")
		if !notif.Spec.DryRun {
			if err := r.sendEmail(ctx, notif.Namespace, notif.Spec.Email, plan.Name, message); err != nil {
				deliveryErrs = append(deliveryErrs, fmt.Errorf("email: %w", err))
			}
		}
	}
	if len(sentChannels) == 0 {
		notif.Status.LastMessage = "Enable at least one channel (slack or email)"
		setNotificationCondition(&notif, "Ready", metav1.ConditionFalse, "NoChannelsConfigured", notif.Status.LastMessage)
		if err := r.Status().Update(ctx, &notif); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
	}

	nowMeta := metav1.NewTime(now)
	notif.Status.LastSentTime = &nowMeta
	notif.Status.LastDigestHash = snapshotHash
	notif.Status.LastSentChannels = sentChannels
	if notif.Spec.DryRun {
		notif.Status.LastMessage = "dry-run notification emitted"
		setNotificationCondition(&notif, "Ready", metav1.ConditionTrue, "DryRun", "Dry-run enabled; no external delivery attempted")
	} else if len(deliveryErrs) > 0 {
		notif.Status.LastMessage = strings.Join(errorsToStrings(deliveryErrs), "; ")
		setNotificationCondition(&notif, "Ready", metav1.ConditionFalse, "DeliveryFailed", notif.Status.LastMessage)
	} else {
		notif.Status.LastMessage = fmt.Sprintf("Notification sent to %s", strings.Join(sentChannels, ","))
		setNotificationCondition(&notif, "Ready", metav1.ConditionTrue, "Delivered", notif.Status.LastMessage)
	}

	if err := r.Status().Update(ctx, &notif); err != nil {
		return ctrl.Result{}, err
	}
	if len(deliveryErrs) > 0 && !notif.Spec.DryRun {
		logger.Error(deliveryErrs[0], "notification delivery had failures")
	}
	return ctrl.Result{RequeueAfter: defaultNotificationRequeue}, nil
}

func (r *CapacityPlanNotificationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capacityv1.CapacityPlanNotification{}).
		Named("capacity-plan-notification").
		Complete(r)
}

func (r *CapacityPlanNotificationReconciler) sendSlack(
	ctx context.Context,
	namespace string,
	spec capacityv1.SlackNotificationSpec,
	message string,
) error {
	secretName := strings.TrimSpace(spec.WebhookSecretRefName)
	if secretName == "" {
		return fmt.Errorf("webhookSecretRefName is required")
	}
	key := defaultSecretKey(spec.WebhookSecretKey, "webhookURL")
	webhookURL, err := r.readSecretValueFromNamespace(ctx, namespace, secretName, key)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"text": message})
	if err != nil {
		return err
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (r *CapacityPlanNotificationReconciler) sendEmail(
	ctx context.Context,
	namespace string,
	spec capacityv1.EmailNotificationSpec,
	planName string,
	message string,
) error {
	if len(spec.To) == 0 {
		return fmt.Errorf("email recipients are required")
	}
	secretName := strings.TrimSpace(spec.SMTPSecretRefName)
	if secretName == "" {
		return fmt.Errorf("smtpSecretRefName is required")
	}
	host, err := r.readSecretValueFromNamespace(ctx, namespace, secretName, defaultSecretKey(spec.SMTPHostKey, "host"))
	if err != nil {
		return err
	}
	portRaw, err := r.readSecretValueFromNamespace(ctx, namespace, secretName, defaultSecretKey(spec.SMTPPortKey, "port"))
	if err != nil {
		return err
	}
	if _, err := strconv.Atoi(portRaw); err != nil {
		return fmt.Errorf("invalid smtp port %q: %w", portRaw, err)
	}
	username, err := r.readSecretValueFromNamespace(ctx, namespace, secretName, defaultSecretKey(spec.SMTPUsernameKey, "username"))
	if err != nil {
		return err
	}
	password, err := r.readSecretValueFromNamespace(ctx, namespace, secretName, defaultSecretKey(spec.SMTPPasswordKey, "password"))
	if err != nil {
		return err
	}
	from := strings.TrimSpace(spec.From)
	if from == "" {
		from = username
	}
	if from == "" {
		return fmt.Errorf("email from address is required")
	}
	subjectPrefix := strings.TrimSpace(spec.SubjectPrefix)
	if subjectPrefix == "" {
		subjectPrefix = "[capacity-plan]"
	}
	subject := fmt.Sprintf("%s %s risk digest", subjectPrefix, planName)
	msg := []byte("To: " + strings.Join(spec.To, ",") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" + message + "\r\n")

	addr := host + ":" + portRaw
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	return smtp.SendMail(addr, auth, from, spec.To, msg)
}

func (r *CapacityPlanNotificationReconciler) readSecretValueFromNamespace(
	ctx context.Context,
	namespace string,
	name string,
	key string,
) (string, error) {
	name = strings.TrimSpace(name)
	key = strings.TrimSpace(key)
	if name == "" || key == "" {
		return "", fmt.Errorf("secret name/key required")
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return "", err
	}
	val, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s missing key %q", namespace, name, key)
	}
	s := strings.TrimSpace(string(val))
	if s == "" {
		return "", fmt.Errorf("secret %s/%s key %q is empty", namespace, name, key)
	}
	return s, nil
}

func (r *CapacityPlanNotificationReconciler) renderNotificationMessage(plan *capacityv1.CapacityPlan) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "CapacityPlan %s\n", plan.Name)
	if strings.TrimSpace(plan.Status.RiskDigest) != "" {
		_, _ = b.WriteString(strings.TrimSpace(plan.Status.RiskDigest))
		_, _ = b.WriteString("\n")
	}
	if strings.TrimSpace(plan.Status.RiskChangeSummary) != "" {
		_, _ = fmt.Fprintf(&b, "%s\n", strings.TrimSpace(plan.Status.RiskChangeSummary))
	}
	for _, r := range plan.Status.TopRisks {
		line := fmt.Sprintf("- %s/%s: %.2f GiB/day", r.Namespace, r.Name, r.WeeklyGrowthBytesPerDay/float64(1024*1024*1024))
		if r.ProjectedFullAt != nil {
			line += " full by " + r.ProjectedFullAt.UTC().Format("2006-01-02")
		}
		if strings.TrimSpace(r.WorkloadKind) != "" && strings.TrimSpace(r.WorkloadName) != "" {
			line += fmt.Sprintf(" owner=%s/%s", r.WorkloadKind, r.WorkloadName)
		}
		_, _ = b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

func setNotificationCondition(
	obj *capacityv1.CapacityPlanNotification,
	condType string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) {
	meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: obj.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func errorsToStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		out = append(out, err.Error())
	}
	return out
}
