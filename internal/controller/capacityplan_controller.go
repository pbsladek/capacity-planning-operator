/*
Copyright 2024 pbsladek.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
	"github.com/pbsladek/capacity-planning-operator/internal/llm"
	"github.com/pbsladek/capacity-planning-operator/internal/metrics"
	opmetrics "github.com/pbsladek/capacity-planning-operator/internal/operator"
)

// defaultReconcileInterval is used when spec.reconcileInterval is zero.
const defaultReconcileInterval = time.Hour

// CapacityPlanReconciler reconciles CapacityPlan objects. It reads in-memory
// ring-buffer snapshots from PVCWatcherReconciler, runs OLS growth analysis,
// calls the LLM (rate-limited), updates Prometheus gauges, and reconciles
// optional child resources (PrometheusRule, Grafana ConfigMap).
//
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
type CapacityPlanReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Watcher   *PVCWatcherReconciler
	LLMClient llm.InsightGenerator

	DefaultMetricsClient metrics.PVCMetricsClient
	DefaultRetention     int
	OperatorNamespace    string

	hasPrometheusOperator bool
}

// Reconcile processes a CapacityPlan resource.
func (r *CapacityPlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var plan capacityv1.CapacityPlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	r.configureWatcherForPlan(plan.Spec)

	// Collect all PVCs visible to the watcher that are in-scope.
	pvcs, err := r.listScopedPVCs(ctx, plan.Spec.Namespaces)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Parse the alert threshold ratio.
	usageThreshold := 0.85
	if plan.Spec.Thresholds.UsageRatio != "" {
		if v, parseErr := strconv.ParseFloat(plan.Spec.Thresholds.UsageRatio, 64); parseErr == nil {
			usageThreshold = v
		}
	}
	daysThreshold := float64(plan.Spec.Thresholds.DaysUntilFull)
	if daysThreshold == 0 {
		daysThreshold = 7
	}

	// Build previous-summary index for LLM rate limiting and insight preservation.
	prevByKey := make(map[string]capacityv1.PVCSummary, len(plan.Status.PVCs))
	for _, s := range plan.Status.PVCs {
		prevByKey[s.Namespace+"/"+s.Name] = s
	}

	// LLM interval, with a sensible default.
	llmInterval := plan.Spec.LLMInsightsInterval.Duration
	if llmInterval <= 0 {
		llmInterval = 6 * time.Hour
	}
	activeLLM := r.LLMClient
	if activeLLM == nil {
		activeLLM, err = r.resolvePlanLLMClient(ctx, &plan)
		if err != nil {
			logger.Error(err, "failed to configure LLM provider; insights disabled for this reconcile")
			activeLLM = nil
		}
	}

	// Build new summaries.
	summaries := make([]capacityv1.PVCSummary, 0, len(pvcs))
	for i := range pvcs {
		pvc := &pvcs[i]
		key := pvc.Namespace + "/" + pvc.Name
		summary := r.buildSummary(ctx, pvc, key, usageThreshold, daysThreshold, llmInterval, activeLLM, prevByKey[key])
		summaries = append(summaries, summary)
	}

	// Update Prometheus gauges for each summary.
	for _, s := range summaries {
		labels := []string{s.Namespace, s.Name}
		opmetrics.PVCGrowthBytesPerDay.WithLabelValues(labels...).Set(s.GrowthBytesPerDay)
		daysVal := -1.0
		if s.DaysUntilFull != nil {
			daysVal = *s.DaysUntilFull
		}
		opmetrics.PVCDaysUntilFull.WithLabelValues(labels...).Set(daysVal)
	}

	// Reconcile optional child resources.
	if r.hasPrometheusOperator {
		if err := r.reconcilePrometheusRule(ctx, &plan, usageThreshold, daysThreshold); err != nil {
			logger.Error(err, "failed to reconcile PrometheusRule")
			// Non-fatal: operator continues without alerting integration.
		}
	}
	if err := r.reconcileGrafanaDashboard(ctx, &plan); err != nil {
		logger.Error(err, "failed to reconcile Grafana dashboard")
	}

	// Patch status.
	now := metav1.Now()
	plan.Status.PVCs = summaries
	plan.Status.LastReconcileTime = &now
	plan.Status.ObservedGeneration = plan.Generation
	meta.SetStatusCondition(&plan.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: plan.Generation,
		Reason:             "ReconcileSucceeded",
		Message:            "Capacity plan reconciled successfully",
	})
	if err := r.Status().Update(ctx, &plan); err != nil {
		return ctrl.Result{}, err
	}

	// Schedule next reconcile.
	interval := plan.Spec.ReconcileInterval.Duration
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// buildSummary constructs the PVCSummary for a single PVC by reading its ring-buffer
// snapshot, running OLS regression, and conditionally refreshing the LLM insight.
func (r *CapacityPlanReconciler) buildSummary(
	ctx context.Context,
	pvc *corev1.PersistentVolumeClaim,
	key string,
	usageThreshold float64,
	daysThreshold float64,
	llmInterval time.Duration,
	llmClient llm.InsightGenerator,
	prev capacityv1.PVCSummary,
) capacityv1.PVCSummary {
	logger := log.FromContext(ctx)

	// Capacity from PVC spec.
	var capacityBytes int64
	if qty, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		capacityBytes = qty.Value()
	}

	// Ring-buffer snapshot.
	samples := r.Watcher.GetSnapshot(key)

	// Growth analysis.
	growth := analysis.CalculateGrowth(samples, capacityBytes)

	// Latest usage from the most recent sample, or carry previous.
	var usedBytes int64
	var lastSampleTime *metav1.Time
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		usedBytes = last.UsedBytes
		t := metav1.NewTime(last.Timestamp)
		lastSampleTime = &t
	} else {
		usedBytes = prev.UsedBytes
		lastSampleTime = prev.LastSampleTime
	}

	// Usage ratio.
	var usageRatio float64
	if capacityBytes > 0 {
		usageRatio = float64(usedBytes) / float64(capacityBytes)
	}

	// Alert condition.
	alertFiring := usageRatio >= usageThreshold ||
		(growth.DaysUntilFull != nil && *growth.DaysUntilFull < daysThreshold)

	// LLM insight — preserve previous unless rate-limit window has elapsed.
	insight := prev.LLMInsight
	lastLLMTime := prev.LastLLMTime
	if llmClient != nil {
		needsRefresh := lastLLMTime == nil || time.Since(lastLLMTime.Time) >= llmInterval
		if needsRefresh {
			newInsight, err := llmClient.GenerateInsight(ctx, llm.PVCContext{
				Namespace:     pvc.Namespace,
				Name:          pvc.Name,
				Growth:        growth,
				UsedBytes:     usedBytes,
				CapacityBytes: capacityBytes,
				AlertFiring:   alertFiring,
				Samples:       samples,
			})
			if err != nil {
				logger.V(1).Info("LLM insight generation failed, keeping previous", "pvc", key, "error", err)
			} else {
				insight = newInsight
				now := metav1.Now()
				lastLLMTime = &now
			}
		}
	}

	return capacityv1.PVCSummary{
		Namespace:         pvc.Namespace,
		Name:              pvc.Name,
		CapacityBytes:     capacityBytes,
		UsedBytes:         usedBytes,
		UsageRatio:        usageRatio,
		GrowthBytesPerDay: growth.GrowthBytesPerDay,
		DaysUntilFull:     growth.DaysUntilFull,
		ConfidenceR2:      growth.ConfidenceR2,
		SamplesCount:      len(samples),
		LastSampleTime:    lastSampleTime,
		LLMInsight:        insight,
		LastLLMTime:       lastLLMTime,
		AlertFiring:       alertFiring,
	}
}

// listScopedPVCs returns PVCs in the watched namespaces (all if empty).
func (r *CapacityPlanReconciler) listScopedPVCs(ctx context.Context, namespaces []string) ([]corev1.PersistentVolumeClaim, error) {
	if len(namespaces) == 0 {
		var list corev1.PersistentVolumeClaimList
		if err := r.List(ctx, &list); err != nil {
			return nil, err
		}
		return list.Items, nil
	}

	var all []corev1.PersistentVolumeClaim
	for _, ns := range namespaces {
		var list corev1.PersistentVolumeClaimList
		if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return nil, err
		}
		all = append(all, list.Items...)
	}
	return all, nil
}

// reconcilePrometheusRule creates or updates the PrometheusRule child resource
// using unstructured to avoid a hard compile-time dependency on the
// prometheus-operator Go types.
func (r *CapacityPlanReconciler) reconcilePrometheusRule(
	ctx context.Context,
	plan *capacityv1.CapacityPlan,
	usageThreshold float64,
	daysThreshold float64,
) error {
	obj := buildPrometheusRuleUnstructured(plan, usageThreshold, daysThreshold)

	var existing unstructured.Unstructured
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
	if err := r.Get(ctx, key, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, obj)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}

// reconcileGrafanaDashboard creates or updates a ConfigMap that Grafana's
// sidecar picks up automatically via the grafana.com/dashboard label.
func (r *CapacityPlanReconciler) reconcileGrafanaDashboard(ctx context.Context, plan *capacityv1.CapacityPlan) error {
	ns := plan.Spec.GrafanaDashboardNamespace
	if ns == "" {
		ns = r.OperatorNamespace
		if ns == "" {
			ns = os.Getenv("POD_NAMESPACE")
		}
		if ns == "" {
			ns = "default"
		}
	}

	cm := buildGrafanaDashboardConfigMap(ns, plan)

	var existing corev1.ConfigMap
	key := types.NamespacedName{Namespace: ns, Name: cm.Name}
	if err := r.Get(ctx, key, &existing); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		return r.Create(ctx, cm)
	}
	existing.Data = cm.Data
	existing.Labels = cm.Labels
	return r.Update(ctx, &existing)
}

func (r *CapacityPlanReconciler) configureWatcherForPlan(spec capacityv1.CapacityPlanSpec) {
	if r.Watcher == nil {
		return
	}

	retention := spec.SampleRetention
	if retention <= 0 {
		if r.DefaultRetention > 0 {
			retention = r.DefaultRetention
		} else {
			retention = 720
		}
	}

	mc := r.DefaultMetricsClient
	if spec.PrometheusURL != "" {
		mc = metrics.NewPrometheusClient(spec.PrometheusURL)
	}

	r.Watcher.Configure(mc, retention)
}

func (r *CapacityPlanReconciler) resolvePlanLLMClient(ctx context.Context, plan *capacityv1.CapacityPlan) (llm.InsightGenerator, error) {
	cfg, err := r.buildLLMProviderConfig(ctx, plan.Spec.LLM)
	if err != nil {
		return nil, err
	}
	return llm.NewInsightGenerator(cfg)
}

func (r *CapacityPlanReconciler) buildLLMProviderConfig(ctx context.Context, spec capacityv1.LLMProviderSpec) (llm.ProviderConfig, error) {
	provider := strings.TrimSpace(spec.Provider)
	if provider == "" {
		provider = llm.ProviderDisabled
	}

	timeout := spec.Timeout.Duration
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	maxTokens := spec.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 256
	}

	cfg := llm.ProviderConfig{
		Provider:    provider,
		Model:       strings.TrimSpace(spec.Model),
		Timeout:     timeout,
		MaxTokens:   maxTokens,
		Temperature: spec.Temperature,
	}

	switch provider {
	case llm.ProviderDisabled:
		return cfg, nil
	case llm.ProviderOpenAI:
		apiKey, err := r.readSecretValue(ctx, spec.OpenAI.SecretRefName, defaultSecretKey(spec.OpenAI.SecretKey, "apiKey"))
		if err != nil {
			return llm.ProviderConfig{}, err
		}
		cfg.OpenAI = llm.OpenAIConfig{
			APIKey:  apiKey,
			BaseURL: strings.TrimSpace(spec.OpenAI.BaseURL),
		}
	case llm.ProviderAnthropic:
		apiKey, err := r.readSecretValue(ctx, spec.Anthropic.SecretRefName, defaultSecretKey(spec.Anthropic.SecretKey, "apiKey"))
		if err != nil {
			return llm.ProviderConfig{}, err
		}
		cfg.Anthropic = llm.AnthropicConfig{
			APIKey:  apiKey,
			BaseURL: strings.TrimSpace(spec.Anthropic.BaseURL),
		}
	case llm.ProviderFastAPI:
		authToken := ""
		if strings.TrimSpace(spec.FastAPI.AuthSecretRefName) != "" {
			token, err := r.readSecretValue(ctx, spec.FastAPI.AuthSecretRefName, defaultSecretKey(spec.FastAPI.AuthSecretKey, "token"))
			if err != nil {
				return llm.ProviderConfig{}, err
			}
			authToken = token
		}
		cfg.FastAPI = llm.FastAPIConfig{
			URL:           strings.TrimSpace(spec.FastAPI.URL),
			AuthToken:     authToken,
			TLSSkipVerify: spec.FastAPI.TLSSkipVerify,
		}
	default:
		return llm.ProviderConfig{}, fmt.Errorf("unsupported llm provider %q", provider)
	}

	return cfg, nil
}

func (r *CapacityPlanReconciler) readSecretValue(ctx context.Context, name, key string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("secret name is required")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("secret key is required for secret %q", name)
	}

	ns := r.OperatorNamespace
	if ns == "" {
		ns = os.Getenv("POD_NAMESPACE")
	}
	if ns == "" {
		ns = "default"
	}

	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sec); err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", ns, name, err)
	}

	val, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s missing key %q", ns, name, key)
	}
	str := strings.TrimSpace(string(val))
	if str == "" {
		return "", fmt.Errorf("secret %s/%s key %q is empty", ns, name, key)
	}
	return str, nil
}

func defaultSecretKey(v string, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

// SetupWithManager registers the reconciler and detects optional Prometheus
// Operator CRDs via the REST mapper.
func (r *CapacityPlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Detect prometheus-operator ServiceMonitor CRD.
	_, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}, "v1",
	)
	r.hasPrometheusOperator = (err == nil)

	return ctrl.NewControllerManagedBy(mgr).
		For(&capacityv1.CapacityPlan{}).
		Named("capacity-plan").
		Complete(r)
}

// buildPrometheusRuleUnstructured constructs the PrometheusRule manifest body.
func buildPrometheusRuleUnstructured(
	plan *capacityv1.CapacityPlan,
	usageThreshold float64,
	daysThreshold float64,
) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Version: "v1",
		Kind:    "PrometheusRule",
	})
	obj.SetName("capacityplan-" + plan.Name)
	obj.SetNamespace("default")
	obj.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "capacity-plan-operator",
	})

	usageExpr := strconv.FormatFloat(usageThreshold, 'f', 2, 64)
	criticalExpr := "0.95"
	daysExpr := strconv.FormatFloat(daysThreshold, 'f', 0, 64)
	criticalDaysExpr := "3"

	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		map[string]interface{}{
			"name": "capacityplan.pvc",
			"rules": []interface{}{
				buildAlertRule("PVCUsageHigh",
					"capacityplan_pvc_usage_ratio > "+usageExpr,
					"5m", "warning"),
				buildAlertRule("PVCUsageCritical",
					"capacityplan_pvc_usage_ratio > "+criticalExpr,
					"2m", "critical"),
				buildAlertRule("PVCFillingUpSoon",
					"capacityplan_pvc_days_until_full < "+daysExpr+" and capacityplan_pvc_days_until_full >= 0",
					"10m", "warning"),
				buildAlertRule("PVCFillingUpCritical",
					"capacityplan_pvc_days_until_full < "+criticalDaysExpr+" and capacityplan_pvc_days_until_full >= 0",
					"5m", "critical"),
			},
		},
	}, "spec", "groups")

	return obj
}

func buildAlertRule(alert, expr, forDur, severity string) map[string]interface{} {
	return map[string]interface{}{
		"alert": alert,
		"expr":  expr,
		"for":   forDur,
		"labels": map[string]interface{}{
			"severity": severity,
		},
		"annotations": map[string]interface{}{
			"summary": alert + " on {{ $labels.namespace }}/{{ $labels.pvc }}",
		},
	}
}

// buildGrafanaDashboardConfigMap returns a ConfigMap containing a minimal
// Grafana dashboard JSON, labelled for Grafana's sidecar auto-discovery.
func buildGrafanaDashboardConfigMap(ns string, plan *capacityv1.CapacityPlan) *corev1.ConfigMap {
	dashboard := strings.TrimSpace(`{
  "title": "PVC Capacity Planning",
  "uid": "capacityplan-pvc",
  "panels": [
    {"type":"timeseries","title":"PVC Usage Ratio","targets":[{"expr":"capacityplan_pvc_usage_ratio"}]},
    {"type":"timeseries","title":"PVC Growth (bytes/day)","targets":[{"expr":"capacityplan_pvc_growth_bytes_per_day"}]},
    {"type":"gauge","title":"Days Until Full","targets":[{"expr":"capacityplan_pvc_days_until_full >= 0"}]},
    {"type":"stat","title":"PVCs Filling Up Soon","targets":[{"expr":"count(capacityplan_pvc_days_until_full < 7 and capacityplan_pvc_days_until_full >= 0)"}]},
    {"type":"table","title":"PVCs by Usage","targets":[{"expr":"sort_desc(capacityplan_pvc_usage_ratio)"}]}
  ]
}`)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "capacityplan-dashboard-" + plan.Name,
			Namespace: ns,
			Labels: map[string]string{
				"grafana.com/dashboard":        "1",
				"app.kubernetes.io/managed-by": "capacity-plan-operator",
			},
		},
		Data: map[string]string{
			"dashboard.json": dashboard,
		},
	}
}
