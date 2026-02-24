package cirunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type NightlyAlertDeliveryRunner struct {
	cfg     Config
	clients *Clients

	alertReceiverPF *PortForwardSession
}

func NewNightlyAlertDeliveryRunner(cfg Config) (*NightlyAlertDeliveryRunner, error) {
	clients, err := BuildClients()
	if err != nil {
		return nil, err
	}
	return &NightlyAlertDeliveryRunner{
		cfg:     cfg,
		clients: clients,
	}, nil
}

func (r *NightlyAlertDeliveryRunner) closePortForwards() {
	if r.alertReceiverPF != nil {
		r.alertReceiverPF.Close()
	}
}

func (r *NightlyAlertDeliveryRunner) startAlertReceiverPortForward(ctx context.Context) error {
	pod, err := getFirstPodByLabel(ctx, r.clients, r.cfg.MonitoringNamespace, map[string]string{"app": "alert-receiver"})
	if err != nil {
		return err
	}
	localPort := r.cfg.NightlyAlertReceiverPort
	if localPort <= 0 {
		localPort = 29080
	}
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, pod.Name, localPort, 8080)
	if err != nil {
		return err
	}
	r.alertReceiverPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return err
	}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", localPort)
	return waitUntil(
		ctx,
		time.Duration(r.cfg.AlertEndpointReadyTimeout)*time.Second,
		r.cfg.PollInterval(),
		"alert-receiver /healthz",
		func(ctx context.Context) (bool, error) {
			_, err := httpBody(ctx, healthURL, 5*time.Second)
			if err != nil {
				return false, nil
			}
			return true, nil
		},
	)
}

func containsSyntheticAlert(records []string, alertName string) bool {
	for _, record := range records {
		if strings.Contains(record, alertName) {
			return true
		}
	}
	return false
}

func (r *NightlyAlertDeliveryRunner) fetchReceiverRecords(ctx context.Context) (alertReceiverRecords, string, error) {
	localPort := r.cfg.NightlyAlertReceiverPort
	if localPort <= 0 {
		localPort = 29080
	}
	recordsURL := fmt.Sprintf("http://127.0.0.1:%d/records", localPort)
	body, err := httpBody(ctx, recordsURL, 5*time.Second)
	if err != nil {
		return alertReceiverRecords{}, "", err
	}
	records, err := parseAlertReceiverRecords(body)
	if err != nil {
		return alertReceiverRecords{}, body, err
	}
	return records, body, nil
}

func (r *NightlyAlertDeliveryRunner) applySyntheticRule(ctx context.Context) error {
	ruleName := strings.TrimSpace(r.cfg.NightlyRuleName)
	if ruleName == "" {
		ruleName = "ci-always-firing"
	}

	rule := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "PrometheusRule",
			"metadata": map[string]any{
				"name":      ruleName,
				"namespace": r.cfg.MonitoringNamespace,
				"labels": map[string]any{
					"release": "kube-prometheus-stack",
				},
			},
			"spec": map[string]any{
				"groups": []any{
					map[string]any{
						"name": "ci.alert.delivery",
						"rules": []any{
							map[string]any{
								"alert": "CIAlwaysFiring",
								"expr":  "vector(1)",
								"for":   "1m",
								"labels": map[string]any{
									"severity": "warning",
								},
								"annotations": map[string]any{
									"summary":     "CI alert delivery test",
									"description": "Synthetic alert for nightly Alertmanager delivery-path validation.",
								},
							},
						},
					},
				},
			},
		},
	}

	gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
	resource := r.clients.Dynamic.Resource(gvr).Namespace(r.cfg.MonitoringNamespace)

	current, err := resource.Get(ctx, ruleName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get prometheusrule/%s: %w", ruleName, err)
		}
		if _, err := resource.Create(ctx, rule, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create prometheusrule/%s: %w", ruleName, err)
		}
		return nil
	}

	rule.SetResourceVersion(current.GetResourceVersion())
	if _, err := resource.Update(ctx, rule, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update prometheusrule/%s: %w", ruleName, err)
	}
	return nil
}

func (r *NightlyAlertDeliveryRunner) Run(ctx context.Context) error {
	defer r.closePortForwards()

	logStep("Ensuring alert-receiver deployment is ready")
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "alert-receiver", 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}

	logStep("Starting alert-receiver port-forward")
	if err := r.startAlertReceiverPortForward(ctx); err != nil {
		return err
	}

	baseline, _, err := r.fetchReceiverRecords(ctx)
	if err != nil {
		return fmt.Errorf("fetch baseline alert-receiver records: %w", err)
	}

	logStep("Applying always-firing PrometheusRule")
	if err := r.applySyntheticRule(ctx); err != nil {
		return err
	}

	logStep("Waiting for webhook delivery")
	alertName := "CIAlwaysFiring"
	timeout := time.Duration(r.cfg.AlertPropagationTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	var finalRecords alertReceiverRecords
	var finalBody string
	err = waitUntil(ctx, timeout, r.cfg.PollInterval(), "synthetic alert delivery via alert-receiver records", func(ctx context.Context) (bool, error) {
		records, body, err := r.fetchReceiverRecords(ctx)
		if err != nil {
			return false, nil
		}
		finalRecords = records
		finalBody = body
		return records.Count > baseline.Count && containsSyntheticAlert(records.Records, alertName), nil
	})
	if err != nil {
		fmt.Println("Alert-receiver port-forward logs:")
		if r.alertReceiverPF != nil {
			fmt.Print(r.alertReceiverPF.Logs())
		}
		fmt.Println("Receiver records payload:")
		if strings.TrimSpace(finalBody) == "" {
			if _, body, fetchErr := r.fetchReceiverRecords(ctx); fetchErr == nil {
				finalBody = body
			}
		}
		if strings.TrimSpace(finalBody) != "" {
			fmt.Println(finalBody)
		}
		return err
	}

	result := map[string]any{
		"baselineCount": baseline.Count,
		"finalCount":    finalRecords.Count,
		"alertName":     alertName,
	}
	raw, _ := json.Marshal(result)
	fmt.Printf("Alert delivery confirmed: %s\n", string(raw))
	return nil
}
