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
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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
const statusSummaryTopN = 3

const (
	riskTrendWindow            = 7 * 24 * time.Hour
	riskAccelerationWindow     = 24 * time.Hour
	maxRiskDigestAnnotation    = 700
	riskEscalationGrowthFactor = 1.20
	riskEscalationDateDelta    = 24 * time.Hour
	anomalyAccelerationSpike   = "acceleration_spike"
	anomalyTrendInstability    = "trend_instability"
	anomalySuddenGrowth        = "sudden_growth"
)

var supportedAnomalyTypes = []string{
	anomalyAccelerationSpike,
	anomalyTrendInstability,
	anomalySuddenGrowth,
}

const (
	conditionTypeReady           = "Ready"
	conditionTypePrometheusReady = "PrometheusReady"
	conditionTypeLLMReady        = "LLMReady"
	conditionTypeBackfillReady   = "BackfillReady"
)

// CapacityPlanReconciler reconciles CapacityPlan objects. It reads in-memory
// ring-buffer snapshots from PVCWatcherReconciler, runs OLS growth analysis,
// calls the LLM (rate-limited), updates Prometheus gauges, and reconciles
// optional child resources (PrometheusRule, Grafana ConfigMap).
//
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch
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

	StartupBackfillConfigured     bool
	StartupBackfillSuccessfulPVCs int
	StartupBackfillError          string

	hasPrometheusOperator bool

	llmCacheMu     sync.RWMutex
	llmClientCache map[string]llm.InsightGenerator
}

// Reconcile processes a CapacityPlan resource.
func (r *CapacityPlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("capacityPlan", req.Name)

	var plan capacityv1.CapacityPlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	isActive, activePlanName, err := r.isActivePlan(ctx, &plan)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !isActive {
		logger.V(1).Info("plan is inactive", "activePlan", activePlanName)
		return r.markPlanInactive(ctx, &plan, activePlanName)
	}
	logger.V(1).Info("reconciling active plan", "generation", plan.Generation)
	prevTopRisks := append([]capacityv1.PVCRiskSummary(nil), plan.Status.TopRisks...)
	r.configureWatcherForPlan(plan.Spec)

	// Collect all PVCs visible to the watcher that are in-scope.
	pvcs, err := r.listScopedPVCs(ctx, plan.Spec.Namespaces)
	if err != nil {
		return ctrl.Result{}, err
	}
	logger.V(1).Info("listed scoped PVCs", "count", len(pvcs))

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
	llmProviderLabel := "disabled"
	llmModelLabel := "n/a"
	llmCondStatus := metav1.ConditionFalse
	llmCondReason := "Disabled"
	llmCondMsg := "LLM provider is disabled"
	if activeLLM == nil {
		activeLLM, llmProviderLabel, llmModelLabel, err = r.resolvePlanLLMClient(ctx, &plan)
		if err != nil {
			logger.Error(err, "failed to configure LLM provider; insights disabled for this reconcile")
			activeLLM = nil
			llmProviderLabel = "disabled"
			llmModelLabel = "n/a"
			llmCondStatus = metav1.ConditionFalse
			llmCondReason = "ProviderError"
			llmCondMsg = err.Error()
		} else if activeLLM != nil {
			llmCondStatus = metav1.ConditionTrue
			llmCondReason = "Configured"
			llmCondMsg = fmt.Sprintf("LLM provider %q is configured", llmProviderLabel)
		}
	} else {
		llmProviderLabel = "custom"
		llmModelLabel = "custom"
		llmCondStatus = metav1.ConditionTrue
		llmCondReason = "Injected"
		llmCondMsg = "LLM client injected into reconciler"
	}
	if llmProviderLabel == "" {
		llmProviderLabel = "disabled"
	}
	if llmModelLabel == "" {
		llmModelLabel = "n/a"
	}

	// Build new summaries.
	nowTime := time.Now()
	summaries := make([]capacityv1.PVCSummary, 0, len(pvcs))
	snapshotsByKey := make(map[string][]analysis.Sample, len(pvcs))
	for i := range pvcs {
		pvc := &pvcs[i]
		key := pvc.Namespace + "/" + pvc.Name
		samples := r.Watcher.GetSnapshot(key)
		snapshotsByKey[key] = samples
		summary := r.buildSummary(
			ctx, pvc, key, samples, usageThreshold, daysThreshold, llmInterval, activeLLM, llmProviderLabel, llmModelLabel, prevByKey[key], plan.Spec.LLM.OnlyAlertingPVCs,
		)
		summaries = append(summaries, summary)
	}
	riskSignals := buildPVCRiskSignals(nowTime, summaries, snapshotsByKey)
	riskByKey := make(map[string]pvcRiskSignal, len(riskSignals))
	for i := range riskSignals {
		sig := riskSignals[i]
		riskByKey[sig.Namespace+"/"+sig.Name] = sig
	}
	workloadsByPVC, workloadErr := r.buildPVCWorkloadIndex(ctx, summaries)
	if workloadErr != nil {
		logger.V(1).Info("workload index built with partial errors", "error", workloadErr)
	}
	topRisks := buildTopRisks(riskSignals, statusSummaryTopN)
	enrichTopRisksWithWorkloads(topRisks, workloadsByPVC)
	namespaceForecasts, workloadForecasts := buildBudgetForecasts(nowTime, plan.Spec.Budgets, summaries, riskByKey, workloadsByPVC)
	anomalies := detectPVCAnomalies(nowTime, riskSignals, workloadsByPVC)
	anomalySummary := summarizePVCAnomalies(anomalies)
	riskDigest := buildRiskDigest(nowTime, topRisks)
	riskChanges := detectRiskChanges(nowTime, prevTopRisks, topRisks)
	riskChangeSummary := summarizeRiskChanges(riskChanges)
	riskSnapshotHash := computeRiskSnapshotHash(topRisks)

	// Update Prometheus gauges for each summary.
	for _, s := range summaries {
		labels := []string{s.Namespace, s.Name}
		opmetrics.PVCGrowthBytesPerDay.WithLabelValues(labels...).Set(s.GrowthBytesPerDay)
		daysVal := -1.0
		if s.DaysUntilFull != nil {
			daysVal = *s.DaysUntilFull
		}
		opmetrics.PVCDaysUntilFull.WithLabelValues(labels...).Set(daysVal)
		sig := riskByKey[s.Namespace+"/"+s.Name]
		opmetrics.PVCGrowthAcceleration.WithLabelValues(labels...).Set(sig.GrowthAcceleration)
		projectedVal := -1.0
		if sig.ProjectedFullAt != nil {
			projectedVal = float64(sig.ProjectedFullAt.Time.Unix())
		}
		opmetrics.PVCProjectedFullTimestampSeconds.WithLabelValues(labels...).Set(projectedVal)
		for _, typ := range supportedAnomalyTypes {
			opmetrics.PVCAnomaly.WithLabelValues(s.Namespace, s.Name, typ).Set(0)
		}
	}
	for _, c := range riskChanges {
		opmetrics.PVCRiskChangesTotal.WithLabelValues(c.Type).Inc()
	}
	for _, a := range anomalies {
		opmetrics.PVCAnomaly.WithLabelValues(a.Namespace, a.Name, a.Type).Set(1)
		opmetrics.PVCAnomaliesTotal.WithLabelValues(a.Type).Inc()
	}
	opmetrics.NamespaceBudgetDaysToBreach.Reset()
	for _, f := range namespaceForecasts {
		val := -1.0
		if f.DaysUntilBreach != nil {
			val = *f.DaysUntilBreach
		}
		opmetrics.NamespaceBudgetDaysToBreach.WithLabelValues(f.Namespace).Set(val)
	}
	opmetrics.WorkloadBudgetDaysToBreach.Reset()
	for _, f := range workloadForecasts {
		val := -1.0
		if f.DaysUntilBreach != nil {
			val = *f.DaysUntilBreach
		}
		opmetrics.WorkloadBudgetDaysToBreach.WithLabelValues(f.Namespace, f.Kind, f.Name).Set(val)
	}

	// Reconcile optional child resources.
	if r.hasPrometheusOperator {
		if err := r.reconcilePrometheusRule(ctx, &plan, usageThreshold, daysThreshold, riskDigest); err != nil {
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
	plan.Status.Summary = buildStatusSummary(summaries, statusSummaryTopN)
	plan.Status.TopRisks = topRisks
	plan.Status.RiskDigest = riskDigest
	plan.Status.RiskChanges = riskChanges
	plan.Status.RiskChangeSummary = riskChangeSummary
	plan.Status.RiskSnapshotHash = riskSnapshotHash
	plan.Status.NamespaceForecasts = namespaceForecasts
	plan.Status.WorkloadForecasts = workloadForecasts
	plan.Status.Anomalies = anomalies
	plan.Status.AnomalySummary = anomalySummary
	plan.Status.LastReconcileTime = &now
	plan.Status.ObservedGeneration = plan.Generation
	setCondition(&plan, conditionTypeReady, metav1.ConditionTrue, "ReconcileSucceeded", "Capacity plan reconciled successfully")
	promStatus, promReason, promMsg := r.prometheusReadyStatus(plan.Spec)
	setCondition(&plan, conditionTypePrometheusReady, promStatus, promReason, promMsg)
	setCondition(&plan, conditionTypeLLMReady, llmCondStatus, llmCondReason, llmCondMsg)
	backfillStatus, backfillReason, backfillMsg := r.backfillReadyStatus()
	setCondition(&plan, conditionTypeBackfillReady, backfillStatus, backfillReason, backfillMsg)
	if err := r.Status().Update(ctx, &plan); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(1).Info("updated capacity plan status",
		"totalPVCs", plan.Status.Summary.TotalPVCs,
		"alertingPVCs", plan.Status.Summary.AlertingPVCs,
		"llmProvider", llmProviderLabel,
	)

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
	samples []analysis.Sample,
	usageThreshold float64,
	daysThreshold float64,
	llmInterval time.Duration,
	llmClient llm.InsightGenerator,
	llmProviderLabel string,
	llmModelLabel string,
	prev capacityv1.PVCSummary,
	onlyAlertingPVCs bool,
) capacityv1.PVCSummary {
	logger := log.FromContext(ctx)

	// Capacity from PVC spec.
	var capacityBytes int64
	if qty, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		capacityBytes = qty.Value()
	}

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
		if onlyAlertingPVCs && !alertFiring {
			logger.V(1).Info("skipping LLM refresh for non-alerting PVC",
				"pvc", key,
				"usageRatio", usageRatio,
			)
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
		needsRefresh := lastLLMTime == nil || time.Since(lastLLMTime.Time) >= llmInterval
		if needsRefresh {
			start := time.Now()
			opmetrics.LLMRequestsTotal.WithLabelValues(llmProviderLabel, llmModelLabel).Inc()
			newInsight, err := llmClient.GenerateInsight(ctx, llm.PVCContext{
				Namespace:     pvc.Namespace,
				Name:          pvc.Name,
				Growth:        growth,
				UsedBytes:     usedBytes,
				CapacityBytes: capacityBytes,
				AlertFiring:   alertFiring,
				Samples:       samples,
			})
			opmetrics.LLMLatencySeconds.WithLabelValues(llmProviderLabel, llmModelLabel).
				Observe(time.Since(start).Seconds())
			if err != nil {
				opmetrics.LLMErrorsTotal.WithLabelValues(llmProviderLabel, llmModelLabel).Inc()
				logger.V(1).Info("LLM insight generation failed, keeping previous", "pvc", key, "error", err)
			} else {
				insight = newInsight
				now := metav1.Now()
				lastLLMTime = &now
				logger.V(1).Info("refreshed LLM insight", "pvc", key, "provider", llmProviderLabel)
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
	riskDigest string,
) error {
	obj := buildPrometheusRuleUnstructured(plan, usageThreshold, daysThreshold, riskDigest)

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

func (r *CapacityPlanReconciler) resolvePlanLLMClient(
	ctx context.Context,
	plan *capacityv1.CapacityPlan,
) (llm.InsightGenerator, string, string, error) {
	cfg, cacheKey, err := r.buildLLMProviderConfig(ctx, plan.Spec.LLM)
	if err != nil {
		return nil, "", "", err
	}

	if cfg.Provider == llm.ProviderDisabled {
		return nil, "disabled", "n/a", nil
	}

	if cacheKey != "" {
		if gen := r.getCachedLLMClient(cacheKey); gen != nil {
			return gen, cfg.Provider, defaultLLMModelLabel(cfg.Model), nil
		}
	}

	gen, err := llm.NewInsightGenerator(cfg)
	if err != nil {
		return nil, "", "", err
	}
	if cacheKey != "" && gen != nil {
		r.setCachedLLMClient(cacheKey, gen)
	}

	return gen, cfg.Provider, defaultLLMModelLabel(cfg.Model), nil
}

func (r *CapacityPlanReconciler) buildLLMProviderConfig(
	ctx context.Context,
	spec capacityv1.LLMProviderSpec,
) (llm.ProviderConfig, string, error) {
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

	cacheKeyParts := []string{
		"provider=" + provider,
		"model=" + cfg.Model,
		"timeout=" + cfg.Timeout.String(),
		fmt.Sprintf("maxTokens=%d", cfg.MaxTokens),
	}
	if cfg.Temperature != nil {
		cacheKeyParts = append(cacheKeyParts, fmt.Sprintf("temp=%.6f", *cfg.Temperature))
	}

	switch provider {
	case llm.ProviderDisabled:
		return cfg, "", nil
	case llm.ProviderOpenAI:
		apiKey, secretRV, err := r.readSecretValue(ctx, spec.OpenAI.SecretRefName, defaultSecretKey(spec.OpenAI.SecretKey, "apiKey"))
		if err != nil {
			return llm.ProviderConfig{}, "", err
		}
		cfg.OpenAI = llm.OpenAIConfig{
			APIKey:  apiKey,
			BaseURL: strings.TrimSpace(spec.OpenAI.BaseURL),
		}
		cacheKeyParts = append(cacheKeyParts,
			"openai.baseURL="+cfg.OpenAI.BaseURL,
			"openai.secret="+strings.TrimSpace(spec.OpenAI.SecretRefName),
			"openai.secretKey="+defaultSecretKey(spec.OpenAI.SecretKey, "apiKey"),
			"openai.secretRV="+secretRV,
		)
	case llm.ProviderAnthropic:
		apiKey, secretRV, err := r.readSecretValue(ctx, spec.Anthropic.SecretRefName, defaultSecretKey(spec.Anthropic.SecretKey, "apiKey"))
		if err != nil {
			return llm.ProviderConfig{}, "", err
		}
		cfg.Anthropic = llm.AnthropicConfig{
			APIKey:  apiKey,
			BaseURL: strings.TrimSpace(spec.Anthropic.BaseURL),
		}
		cacheKeyParts = append(cacheKeyParts,
			"anthropic.baseURL="+cfg.Anthropic.BaseURL,
			"anthropic.secret="+strings.TrimSpace(spec.Anthropic.SecretRefName),
			"anthropic.secretKey="+defaultSecretKey(spec.Anthropic.SecretKey, "apiKey"),
			"anthropic.secretRV="+secretRV,
		)
	case llm.ProviderFastAPI:
		authToken := ""
		authSecretRV := ""
		if strings.TrimSpace(spec.FastAPI.AuthSecretRefName) != "" {
			token, secretRV, err := r.readSecretValue(ctx, spec.FastAPI.AuthSecretRefName, defaultSecretKey(spec.FastAPI.AuthSecretKey, "token"))
			if err != nil {
				return llm.ProviderConfig{}, "", err
			}
			authToken = token
			authSecretRV = secretRV
		}
		cfg.FastAPI = llm.FastAPIConfig{
			URL:              strings.TrimSpace(spec.FastAPI.URL),
			AuthToken:        authToken,
			TLSSkipVerify:    spec.FastAPI.TLSSkipVerify,
			HealthURL:        strings.TrimSpace(spec.FastAPI.HealthURL),
			FailureThreshold: spec.FastAPI.FailureThreshold,
			Cooldown:         spec.FastAPI.Cooldown.Duration,
		}
		cacheKeyParts = append(cacheKeyParts,
			"fastapi.url="+cfg.FastAPI.URL,
			fmt.Sprintf("fastapi.tlsSkipVerify=%t", cfg.FastAPI.TLSSkipVerify),
			"fastapi.healthURL="+cfg.FastAPI.HealthURL,
			fmt.Sprintf("fastapi.failureThreshold=%d", cfg.FastAPI.FailureThreshold),
			"fastapi.cooldown="+cfg.FastAPI.Cooldown.String(),
			"fastapi.authSecret="+strings.TrimSpace(spec.FastAPI.AuthSecretRefName),
			"fastapi.authSecretKey="+defaultSecretKey(spec.FastAPI.AuthSecretKey, "token"),
			"fastapi.authSecretRV="+authSecretRV,
		)
	default:
		return llm.ProviderConfig{}, "", fmt.Errorf("unsupported llm provider %q", provider)
	}

	return cfg, strings.Join(cacheKeyParts, "|"), nil
}

func (r *CapacityPlanReconciler) readSecretValue(ctx context.Context, name, key string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", fmt.Errorf("secret name is required")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", fmt.Errorf("secret key is required for secret %q", name)
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
		return "", "", fmt.Errorf("getting secret %s/%s: %w", ns, name, err)
	}

	val, ok := sec.Data[key]
	if !ok {
		return "", "", fmt.Errorf("secret %s/%s missing key %q", ns, name, key)
	}
	str := strings.TrimSpace(string(val))
	if str == "" {
		return "", "", fmt.Errorf("secret %s/%s key %q is empty", ns, name, key)
	}
	return str, sec.ResourceVersion, nil
}

func defaultSecretKey(v string, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func defaultLLMModelLabel(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "default"
	}
	return v
}

func (r *CapacityPlanReconciler) getCachedLLMClient(key string) llm.InsightGenerator {
	r.llmCacheMu.RLock()
	defer r.llmCacheMu.RUnlock()
	if r.llmClientCache == nil {
		return nil
	}
	return r.llmClientCache[key]
}

func (r *CapacityPlanReconciler) setCachedLLMClient(key string, gen llm.InsightGenerator) {
	r.llmCacheMu.Lock()
	defer r.llmCacheMu.Unlock()
	if r.llmClientCache == nil {
		r.llmClientCache = make(map[string]llm.InsightGenerator)
	}
	r.llmClientCache[key] = gen
}

func (r *CapacityPlanReconciler) isActivePlan(ctx context.Context, plan *capacityv1.CapacityPlan) (bool, string, error) {
	var list capacityv1.CapacityPlanList
	if err := r.List(ctx, &list); err != nil {
		return false, "", err
	}

	active := pickActivePlan(list.Items)
	if active == nil {
		return true, plan.Name, nil
	}
	return plan.Name == active.Name, active.Name, nil
}

func (r *CapacityPlanReconciler) markPlanInactive(
	ctx context.Context,
	plan *capacityv1.CapacityPlan,
	activePlanName string,
) (ctrl.Result, error) {
	now := metav1.Now()
	plan.Status.PVCs = nil
	plan.Status.Summary = capacityv1.CapacityPlanSummary{}
	plan.Status.TopRisks = nil
	plan.Status.RiskDigest = ""
	plan.Status.RiskChanges = nil
	plan.Status.RiskChangeSummary = ""
	plan.Status.RiskSnapshotHash = ""
	plan.Status.NamespaceForecasts = nil
	plan.Status.WorkloadForecasts = nil
	plan.Status.Anomalies = nil
	plan.Status.AnomalySummary = ""
	plan.Status.LastReconcileTime = &now
	plan.Status.ObservedGeneration = plan.Generation
	msg := fmt.Sprintf("Plan is inactive; active plan is %q", activePlanName)
	setCondition(plan, conditionTypeReady, metav1.ConditionFalse, "NotActivePlan", msg)
	setCondition(plan, conditionTypePrometheusReady, metav1.ConditionUnknown, "NotActivePlan", msg)
	setCondition(plan, conditionTypeLLMReady, metav1.ConditionUnknown, "NotActivePlan", msg)
	setCondition(plan, conditionTypeBackfillReady, metav1.ConditionUnknown, "NotActivePlan", msg)
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}

	interval := plan.Spec.ReconcileInterval.Duration
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

func pickActivePlan(plans []capacityv1.CapacityPlan) *capacityv1.CapacityPlan {
	if len(plans) == 0 {
		return nil
	}
	sorted := make([]capacityv1.CapacityPlan, len(plans))
	copy(sorted, plans)
	sort.Slice(sorted, func(i, j int) bool {
		ti := sorted[i].CreationTimestamp.Time
		tj := sorted[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return sorted[i].Name < sorted[j].Name
	})
	return &sorted[0]
}

func buildStatusSummary(items []capacityv1.PVCSummary, topN int) capacityv1.CapacityPlanSummary {
	if topN <= 0 {
		topN = statusSummaryTopN
	}

	out := capacityv1.CapacityPlanSummary{
		TotalPVCs: len(items),
	}

	for _, s := range items {
		if s.AlertFiring {
			out.AlertingPVCs++
		}
	}

	usageRank := make([]capacityv1.PVCSummary, len(items))
	copy(usageRank, items)
	sort.Slice(usageRank, func(i, j int) bool {
		if usageRank[i].UsageRatio == usageRank[j].UsageRatio {
			if usageRank[i].Namespace == usageRank[j].Namespace {
				return usageRank[i].Name < usageRank[j].Name
			}
			return usageRank[i].Namespace < usageRank[j].Namespace
		}
		return usageRank[i].UsageRatio > usageRank[j].UsageRatio
	})
	for i := 0; i < len(usageRank) && i < topN; i++ {
		out.TopByUsage = append(out.TopByUsage, toBrief(usageRank[i]))
	}

	soonest := make([]capacityv1.PVCSummary, 0, len(items))
	for _, s := range items {
		if s.DaysUntilFull != nil {
			soonest = append(soonest, s)
		}
	}
	sort.Slice(soonest, func(i, j int) bool {
		di := *soonest[i].DaysUntilFull
		dj := *soonest[j].DaysUntilFull
		if di == dj {
			if soonest[i].Namespace == soonest[j].Namespace {
				return soonest[i].Name < soonest[j].Name
			}
			return soonest[i].Namespace < soonest[j].Namespace
		}
		return di < dj
	})
	for i := 0; i < len(soonest) && i < topN; i++ {
		out.TopBySoonestToFull = append(out.TopBySoonestToFull, toBrief(soonest[i]))
	}

	growthRank := make([]capacityv1.PVCSummary, 0, len(items))
	for _, s := range items {
		if s.GrowthBytesPerDay > 0 {
			growthRank = append(growthRank, s)
		}
	}
	sort.Slice(growthRank, func(i, j int) bool {
		if growthRank[i].GrowthBytesPerDay == growthRank[j].GrowthBytesPerDay {
			if growthRank[i].Namespace == growthRank[j].Namespace {
				return growthRank[i].Name < growthRank[j].Name
			}
			return growthRank[i].Namespace < growthRank[j].Namespace
		}
		return growthRank[i].GrowthBytesPerDay > growthRank[j].GrowthBytesPerDay
	})
	for i := 0; i < len(growthRank) && i < topN; i++ {
		out.TopByGrowth = append(out.TopByGrowth, toBrief(growthRank[i]))
	}

	return out
}

type pvcRiskSignal struct {
	Namespace string
	Name      string

	UsedBytes     int64
	CapacityBytes int64
	UsageRatio    float64
	AlertFiring   bool
	ConfidenceR2  float64
	LLMInsight    string

	WeeklyGrowthBytesPerDay float64
	DailyGrowthBytesPerDay  float64
	GrowthAcceleration      float64
	DaysUntilFull           *float64
	ProjectedFullAt         *metav1.Time
}

func buildPVCRiskSignals(
	now time.Time,
	summaries []capacityv1.PVCSummary,
	snapshotsByKey map[string][]analysis.Sample,
) []pvcRiskSignal {
	out := make([]pvcRiskSignal, 0, len(summaries))
	for _, s := range summaries {
		key := s.Namespace + "/" + s.Name
		out = append(out, buildPVCRiskSignal(now, s, snapshotsByKey[key]))
	}
	return out
}

func buildPVCRiskSignal(now time.Time, s capacityv1.PVCSummary, samples []analysis.Sample) pvcRiskSignal {
	weeklySamples := samplesSince(samples, now.Add(-riskTrendWindow))
	if len(weeklySamples) < 2 {
		weeklySamples = samples
	}

	weeklyGrowth := s.GrowthBytesPerDay
	weeklyR2 := s.ConfidenceR2
	if len(weeklySamples) >= 2 {
		weekly := analysis.CalculateGrowth(weeklySamples, s.CapacityBytes)
		weeklyGrowth = weekly.GrowthBytesPerDay
		weeklyR2 = weekly.ConfidenceR2
	}

	dailyGrowth := weeklyGrowth
	dailySamples := samplesSince(samples, now.Add(-riskAccelerationWindow))
	if len(dailySamples) >= 2 {
		daily := analysis.CalculateGrowth(dailySamples, s.CapacityBytes)
		dailyGrowth = daily.GrowthBytesPerDay
	}

	accel := growthAcceleration(weeklyGrowth, dailyGrowth)
	daysUntilFull, projectedFullAt := projectedFullFromGrowth(now, s.UsedBytes, s.CapacityBytes, weeklyGrowth)

	return pvcRiskSignal{
		Namespace:               s.Namespace,
		Name:                    s.Name,
		UsedBytes:               s.UsedBytes,
		CapacityBytes:           s.CapacityBytes,
		UsageRatio:              s.UsageRatio,
		AlertFiring:             s.AlertFiring,
		ConfidenceR2:            weeklyR2,
		LLMInsight:              strings.TrimSpace(s.LLMInsight),
		WeeklyGrowthBytesPerDay: weeklyGrowth,
		DailyGrowthBytesPerDay:  dailyGrowth,
		GrowthAcceleration:      accel,
		DaysUntilFull:           daysUntilFull,
		ProjectedFullAt:         projectedFullAt,
	}
}

func samplesSince(samples []analysis.Sample, since time.Time) []analysis.Sample {
	if len(samples) == 0 {
		return nil
	}
	var out []analysis.Sample
	for _, s := range samples {
		if s.Timestamp.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func projectedFullFromGrowth(
	now time.Time,
	usedBytes int64,
	capacityBytes int64,
	growthBytesPerDay float64,
) (*float64, *metav1.Time) {
	if growthBytesPerDay <= 0 || capacityBytes <= 0 {
		return nil, nil
	}
	remaining := float64(capacityBytes - usedBytes)
	if remaining <= 0 {
		zero := 0.0
		t := metav1.NewTime(now)
		return &zero, &t
	}
	days := remaining / growthBytesPerDay
	projected := metav1.NewTime(now.Add(time.Duration(days * 24 * float64(time.Hour))))
	return &days, &projected
}

func growthAcceleration(weeklyGrowth, dailyGrowth float64) float64 {
	if weeklyGrowth == 0 {
		return 0
	}
	return (dailyGrowth - weeklyGrowth) / math.Abs(weeklyGrowth)
}

func buildTopRisks(signals []pvcRiskSignal, topN int) []capacityv1.PVCRiskSummary {
	if topN <= 0 {
		topN = statusSummaryTopN
	}
	candidates := make([]pvcRiskSignal, 0, len(signals))
	for _, s := range signals {
		if s.WeeklyGrowthBytesPerDay > 0 {
			candidates = append(candidates, s)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].WeeklyGrowthBytesPerDay == candidates[j].WeeklyGrowthBytesPerDay {
			di := math.MaxFloat64
			dj := math.MaxFloat64
			if candidates[i].DaysUntilFull != nil {
				di = *candidates[i].DaysUntilFull
			}
			if candidates[j].DaysUntilFull != nil {
				dj = *candidates[j].DaysUntilFull
			}
			if di == dj {
				if candidates[i].Namespace == candidates[j].Namespace {
					return candidates[i].Name < candidates[j].Name
				}
				return candidates[i].Namespace < candidates[j].Namespace
			}
			return di < dj
		}
		return candidates[i].WeeklyGrowthBytesPerDay > candidates[j].WeeklyGrowthBytesPerDay
	})
	out := make([]capacityv1.PVCRiskSummary, 0, topN)
	for i := 0; i < len(candidates) && i < topN; i++ {
		s := candidates[i]
		out = append(out, capacityv1.PVCRiskSummary{
			Namespace:               s.Namespace,
			Name:                    s.Name,
			UsedBytes:               s.UsedBytes,
			CapacityBytes:           s.CapacityBytes,
			UsageRatio:              s.UsageRatio,
			WeeklyGrowthBytesPerDay: s.WeeklyGrowthBytesPerDay,
			DailyGrowthBytesPerDay:  s.DailyGrowthBytesPerDay,
			GrowthAcceleration:      s.GrowthAcceleration,
			ConfidenceR2:            s.ConfidenceR2,
			DaysUntilFull:           s.DaysUntilFull,
			ProjectedFullAt:         s.ProjectedFullAt,
			LLMInsight:              s.LLMInsight,
			AlertFiring:             s.AlertFiring,
		})
	}
	return out
}

func buildRiskDigest(now time.Time, topRisks []capacityv1.PVCRiskSummary) string {
	if len(topRisks) == 0 {
		return "No PVCs currently show a positive weekly growth trend."
	}
	parts := make([]string, 0, len(topRisks))
	for _, r := range topRisks {
		entry := fmt.Sprintf("%s/%s %+0.1f GiB/day",
			r.Namespace,
			r.Name,
			r.WeeklyGrowthBytesPerDay/float64(1024*1024*1024),
		)
		if strings.TrimSpace(r.WorkloadKind) != "" && strings.TrimSpace(r.WorkloadName) != "" {
			entry += fmt.Sprintf(" [owner %s/%s]", r.WorkloadKind, r.WorkloadName)
		}
		if r.ProjectedFullAt != nil {
			entry += " (full by " + r.ProjectedFullAt.UTC().Format("2006-01-02") + ")"
		}
		if r.GrowthAcceleration > 0.15 {
			entry += fmt.Sprintf(", accelerating %+0.0f%%", r.GrowthAcceleration*100)
		}
		if r.ConfidenceR2 > 0 {
			entry += fmt.Sprintf(", R2 %.2f", r.ConfidenceR2)
		}
		if strings.TrimSpace(r.LLMInsight) != "" {
			entry += ", insight: " + trimForAnnotation(strings.TrimSpace(r.LLMInsight), 90)
		}
		parts = append(parts, entry)
	}
	return fmt.Sprintf(
		"Top PVC growth trends (weekly basis, as of %s): %s.",
		now.UTC().Format("2006-01-02"),
		strings.Join(parts, "; "),
	)
}

func detectRiskChanges(
	now time.Time,
	previous []capacityv1.PVCRiskSummary,
	current []capacityv1.PVCRiskSummary,
) []capacityv1.PVCRiskChange {
	prevByKey := make(map[string]capacityv1.PVCRiskSummary, len(previous))
	currByKey := make(map[string]capacityv1.PVCRiskSummary, len(current))
	for _, p := range previous {
		prevByKey[p.Namespace+"/"+p.Name] = p
	}
	for _, c := range current {
		currByKey[c.Namespace+"/"+c.Name] = c
	}

	out := make([]capacityv1.PVCRiskChange, 0, len(previous)+len(current))
	for key, curr := range currByKey {
		prev, ok := prevByKey[key]
		if !ok {
			out = append(out, capacityv1.PVCRiskChange{
				Type:                           "new",
				Namespace:                      curr.Namespace,
				Name:                           curr.Name,
				CurrentWeeklyGrowthBytesPerDay: curr.WeeklyGrowthBytesPerDay,
				CurrentProjectedFullAt:         curr.ProjectedFullAt,
				Message:                        "PVC entered top risk set",
				Time:                           metav1.NewTime(now),
			})
			continue
		}
		if isEscalatedRisk(prev, curr) {
			out = append(out, capacityv1.PVCRiskChange{
				Type:                            "escalated",
				Namespace:                       curr.Namespace,
				Name:                            curr.Name,
				PreviousWeeklyGrowthBytesPerDay: prev.WeeklyGrowthBytesPerDay,
				CurrentWeeklyGrowthBytesPerDay:  curr.WeeklyGrowthBytesPerDay,
				PreviousProjectedFullAt:         prev.ProjectedFullAt,
				CurrentProjectedFullAt:          curr.ProjectedFullAt,
				Message:                         escalatedRiskMessage(prev, curr),
				Time:                            metav1.NewTime(now),
			})
		}
	}
	for key, prev := range prevByKey {
		if _, ok := currByKey[key]; ok {
			continue
		}
		out = append(out, capacityv1.PVCRiskChange{
			Type:                            "recovered",
			Namespace:                       prev.Namespace,
			Name:                            prev.Name,
			PreviousWeeklyGrowthBytesPerDay: prev.WeeklyGrowthBytesPerDay,
			PreviousProjectedFullAt:         prev.ProjectedFullAt,
			Message:                         "PVC dropped out of top risk set",
			Time:                            metav1.NewTime(now),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func isEscalatedRisk(prev, curr capacityv1.PVCRiskSummary) bool {
	if prev.WeeklyGrowthBytesPerDay > 0 &&
		curr.WeeklyGrowthBytesPerDay > prev.WeeklyGrowthBytesPerDay*riskEscalationGrowthFactor {
		return true
	}
	if prev.ProjectedFullAt != nil && curr.ProjectedFullAt != nil {
		if curr.ProjectedFullAt.Time.Before(prev.ProjectedFullAt.Time.Add(-riskEscalationDateDelta)) {
			return true
		}
	}
	return !prev.AlertFiring && curr.AlertFiring
}

func escalatedRiskMessage(prev, curr capacityv1.PVCRiskSummary) string {
	if prev.ProjectedFullAt != nil && curr.ProjectedFullAt != nil &&
		curr.ProjectedFullAt.Time.Before(prev.ProjectedFullAt.Time.Add(-riskEscalationDateDelta)) {
		return fmt.Sprintf(
			"Projected full date moved earlier from %s to %s",
			prev.ProjectedFullAt.UTC().Format("2006-01-02"),
			curr.ProjectedFullAt.UTC().Format("2006-01-02"),
		)
	}
	if prev.WeeklyGrowthBytesPerDay > 0 &&
		curr.WeeklyGrowthBytesPerDay > prev.WeeklyGrowthBytesPerDay*riskEscalationGrowthFactor {
		return fmt.Sprintf(
			"Weekly growth increased from %.2f to %.2f GiB/day",
			prev.WeeklyGrowthBytesPerDay/float64(1024*1024*1024),
			curr.WeeklyGrowthBytesPerDay/float64(1024*1024*1024),
		)
	}
	return "PVC moved into alert-firing state"
}

func summarizeRiskChanges(changes []capacityv1.PVCRiskChange) string {
	if len(changes) == 0 {
		return "No risk changes detected."
	}
	newCount := 0
	escCount := 0
	recCount := 0
	for _, c := range changes {
		switch c.Type {
		case "new":
			newCount++
		case "escalated":
			escCount++
		case "recovered":
			recCount++
		}
	}
	return fmt.Sprintf("Risk changes: new=%d escalated=%d recovered=%d", newCount, escCount, recCount)
}

func computeRiskSnapshotHash(topRisks []capacityv1.PVCRiskSummary) string {
	if len(topRisks) == 0 {
		return ""
	}
	normalized := make([]capacityv1.PVCRiskSummary, len(topRisks))
	copy(normalized, topRisks)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Namespace == normalized[j].Namespace {
			return normalized[i].Name < normalized[j].Name
		}
		return normalized[i].Namespace < normalized[j].Namespace
	})
	var b strings.Builder
	for _, r := range normalized {
		_, _ = fmt.Fprintf(&b, "%s/%s|%f|%f|%t|",
			r.Namespace, r.Name, r.WeeklyGrowthBytesPerDay, r.GrowthAcceleration, r.AlertFiring)
		if r.ProjectedFullAt != nil {
			_, _ = b.WriteString(r.ProjectedFullAt.UTC().Format(time.RFC3339))
		}
		_ = b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

type pvcWorkloadRef struct {
	Kind      string
	Name      string
	Namespace string
}

func enrichTopRisksWithWorkloads(
	topRisks []capacityv1.PVCRiskSummary,
	workloadsByPVC map[string]pvcWorkloadRef,
) {
	for i := range topRisks {
		ref, ok := workloadsByPVC[topRisks[i].Namespace+"/"+topRisks[i].Name]
		if !ok {
			continue
		}
		topRisks[i].WorkloadKind = ref.Kind
		topRisks[i].WorkloadName = ref.Name
		topRisks[i].WorkloadNamespace = ref.Namespace
	}
}

func (r *CapacityPlanReconciler) buildPVCWorkloadIndex(
	ctx context.Context,
	summaries []capacityv1.PVCSummary,
) (map[string]pvcWorkloadRef, error) {
	out := make(map[string]pvcWorkloadRef, len(summaries))
	var errs []error
	for _, s := range summaries {
		key := s.Namespace + "/" + s.Name
		kind, name, ns, err := r.resolvePVCWorkload(ctx, s.Namespace, s.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		out[key] = pvcWorkloadRef{
			Kind:      kind,
			Name:      name,
			Namespace: ns,
		}
	}
	return out, stderrors.Join(errs...)
}

func (r *CapacityPlanReconciler) resolvePVCWorkload(
	ctx context.Context,
	namespace string,
	pvcName string,
) (string, string, string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
		return "", "", "", err
	}

	bestRank := int(^uint(0) >> 1)
	bestKind := ""
	bestName := ""
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podUsesPVC(pod, pvcName) {
			continue
		}
		kind, name, err := r.resolvePodOwner(ctx, pod)
		if err != nil {
			continue
		}
		rank := ownerKindRank(kind)
		if rank < bestRank {
			bestRank = rank
			bestKind = kind
			bestName = name
		}
	}
	if bestKind == "" {
		return "", "", "", nil
	}
	return bestKind, bestName, namespace, nil
}

func podUsesPVC(pod *corev1.Pod, pvcName string) bool {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvcName {
			return true
		}
	}
	return false
}

func ownerKindRank(kind string) int {
	switch kind {
	case "StatefulSet":
		return 1
	case "Deployment":
		return 2
	case "DaemonSet":
		return 3
	case "CronJob":
		return 4
	case "Job":
		return 5
	case "ReplicaSet":
		return 6
	case "Pod":
		return 7
	default:
		return 10
	}
}

func controllerOwnerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		ref := &refs[i]
		if ref.Controller != nil && *ref.Controller {
			return ref
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return &refs[0]
}

func (r *CapacityPlanReconciler) resolvePodOwner(ctx context.Context, pod *corev1.Pod) (string, string, error) {
	ref := controllerOwnerRef(pod.OwnerReferences)
	if ref == nil {
		return "Pod", pod.Name, nil
	}
	switch ref.Kind {
	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}, &rs)
		if err != nil {
			if errors.IsNotFound(err) {
				return "ReplicaSet", ref.Name, nil
			}
			return "", "", err
		}
		rsRef := controllerOwnerRef(rs.OwnerReferences)
		if rsRef != nil && rsRef.Kind == "Deployment" {
			return "Deployment", rsRef.Name, nil
		}
		return "ReplicaSet", rs.Name, nil
	case "Job":
		var job batchv1.Job
		err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}, &job)
		if err != nil {
			if errors.IsNotFound(err) {
				return "Job", ref.Name, nil
			}
			return "", "", err
		}
		jobRef := controllerOwnerRef(job.OwnerReferences)
		if jobRef != nil && jobRef.Kind == "CronJob" {
			return "CronJob", jobRef.Name, nil
		}
		return "Job", job.Name, nil
	default:
		return ref.Kind, ref.Name, nil
	}
}

func buildBudgetForecasts(
	now time.Time,
	spec capacityv1.BudgetSpec,
	summaries []capacityv1.PVCSummary,
	riskByKey map[string]pvcRiskSignal,
	workloadsByPVC map[string]pvcWorkloadRef,
) ([]capacityv1.StorageBudgetForecast, []capacityv1.StorageBudgetForecast) {
	namespaceOut := make([]capacityv1.StorageBudgetForecast, 0, len(spec.NamespaceBudgets))
	workloadOut := make([]capacityv1.StorageBudgetForecast, 0, len(spec.WorkloadBudgets))

	for _, b := range spec.NamespaceBudgets {
		ns := strings.TrimSpace(b.Namespace)
		if ns == "" {
			continue
		}
		budgetBytes, err := parseBudgetBytes(b.Budget)
		if err != nil || budgetBytes <= 0 {
			continue
		}
		used := int64(0)
		growth := 0.0
		for _, s := range summaries {
			if s.Namespace != ns {
				continue
			}
			used += s.UsedBytes
			growth += riskByKey[s.Namespace+"/"+s.Name].WeeklyGrowthBytesPerDay
		}
		days, at := budgetBreachProjection(now, used, budgetBytes, growth)
		ratio := 0.0
		if budgetBytes > 0 {
			ratio = float64(used) / float64(budgetBytes)
		}
		namespaceOut = append(namespaceOut, capacityv1.StorageBudgetForecast{
			Scope:             "namespace",
			Namespace:         ns,
			BudgetBytes:       budgetBytes,
			UsedBytes:         used,
			UsageRatio:        ratio,
			GrowthBytesPerDay: growth,
			DaysUntilBreach:   days,
			ProjectedBreachAt: at,
		})
	}

	for _, b := range spec.WorkloadBudgets {
		ns := strings.TrimSpace(b.Namespace)
		kind := strings.TrimSpace(b.Kind)
		name := strings.TrimSpace(b.Name)
		if ns == "" || kind == "" || name == "" {
			continue
		}
		budgetBytes, err := parseBudgetBytes(b.Budget)
		if err != nil || budgetBytes <= 0 {
			continue
		}
		used := int64(0)
		growth := 0.0
		for _, s := range summaries {
			key := s.Namespace + "/" + s.Name
			ref, ok := workloadsByPVC[key]
			if !ok {
				continue
			}
			if s.Namespace != ns {
				continue
			}
			if !strings.EqualFold(ref.Kind, kind) || ref.Name != name {
				continue
			}
			used += s.UsedBytes
			growth += riskByKey[key].WeeklyGrowthBytesPerDay
		}
		days, at := budgetBreachProjection(now, used, budgetBytes, growth)
		ratio := 0.0
		if budgetBytes > 0 {
			ratio = float64(used) / float64(budgetBytes)
		}
		workloadOut = append(workloadOut, capacityv1.StorageBudgetForecast{
			Scope:             "workload",
			Namespace:         ns,
			Kind:              kind,
			Name:              name,
			BudgetBytes:       budgetBytes,
			UsedBytes:         used,
			UsageRatio:        ratio,
			GrowthBytesPerDay: growth,
			DaysUntilBreach:   days,
			ProjectedBreachAt: at,
		})
	}

	sort.Slice(namespaceOut, func(i, j int) bool {
		return forecastSortLess(namespaceOut[i], namespaceOut[j])
	})
	sort.Slice(workloadOut, func(i, j int) bool {
		return forecastSortLess(workloadOut[i], workloadOut[j])
	})
	return namespaceOut, workloadOut
}

func parseBudgetBytes(raw string) (int64, error) {
	q, err := resource.ParseQuantity(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	return q.Value(), nil
}

func budgetBreachProjection(now time.Time, usedBytes, budgetBytes int64, growthBytesPerDay float64) (*float64, *metav1.Time) {
	if budgetBytes <= 0 {
		return nil, nil
	}
	if usedBytes >= budgetBytes {
		zero := 0.0
		t := metav1.NewTime(now)
		return &zero, &t
	}
	if growthBytesPerDay <= 0 {
		return nil, nil
	}
	days := float64(budgetBytes-usedBytes) / growthBytesPerDay
	if days < 0 {
		days = 0
	}
	t := metav1.NewTime(now.Add(time.Duration(days * 24 * float64(time.Hour))))
	return &days, &t
}

func forecastSortLess(a, b capacityv1.StorageBudgetForecast) bool {
	ad := math.MaxFloat64
	bd := math.MaxFloat64
	if a.DaysUntilBreach != nil {
		ad = *a.DaysUntilBreach
	}
	if b.DaysUntilBreach != nil {
		bd = *b.DaysUntilBreach
	}
	if ad != bd {
		return ad < bd
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Name < b.Name
}

func detectPVCAnomalies(
	now time.Time,
	signals []pvcRiskSignal,
	workloadsByPVC map[string]pvcWorkloadRef,
) []capacityv1.PVCAnomaly {
	out := make([]capacityv1.PVCAnomaly, 0)
	for _, s := range signals {
		ref := workloadsByPVC[s.Namespace+"/"+s.Name]
		if s.WeeklyGrowthBytesPerDay > 0 && s.GrowthAcceleration >= 0.75 {
			sev := "warning"
			if s.GrowthAcceleration >= 1.50 {
				sev = "critical"
			}
			out = append(out, buildAnomaly(now, s, ref, anomalyAccelerationSpike, sev,
				fmt.Sprintf("Daily growth accelerated %+0.0f%% vs weekly baseline", s.GrowthAcceleration*100)))
		}
		if math.Abs(s.WeeklyGrowthBytesPerDay) > float64(10*1024*1024) &&
			s.ConfidenceR2 > 0 && s.ConfidenceR2 < 0.35 {
			out = append(out, buildAnomaly(now, s, ref, anomalyTrendInstability, "warning",
				fmt.Sprintf("Trend confidence dropped to R2 %.2f", s.ConfidenceR2)))
		}
		if s.WeeklyGrowthBytesPerDay > 0 &&
			s.DailyGrowthBytesPerDay > s.WeeklyGrowthBytesPerDay*2.5 &&
			s.DailyGrowthBytesPerDay > float64(100*1024*1024) {
			out = append(out, buildAnomaly(now, s, ref, anomalySuddenGrowth, "critical",
				"Daily growth significantly exceeds weekly trend baseline"))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri := anomalySeverityRank(out[i].Severity)
		rj := anomalySeverityRank(out[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func buildAnomaly(
	now time.Time,
	s pvcRiskSignal,
	ref pvcWorkloadRef,
	typ string,
	severity string,
	message string,
) capacityv1.PVCAnomaly {
	return capacityv1.PVCAnomaly{
		Namespace:               s.Namespace,
		Name:                    s.Name,
		Type:                    typ,
		Severity:                severity,
		Message:                 message,
		Time:                    metav1.NewTime(now),
		WorkloadKind:            ref.Kind,
		WorkloadName:            ref.Name,
		WeeklyGrowthBytesPerDay: s.WeeklyGrowthBytesPerDay,
		DailyGrowthBytesPerDay:  s.DailyGrowthBytesPerDay,
		GrowthAcceleration:      s.GrowthAcceleration,
		ConfidenceR2:            s.ConfidenceR2,
	}
}

func anomalySeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func summarizePVCAnomalies(anomalies []capacityv1.PVCAnomaly) string {
	if len(anomalies) == 0 {
		return "No growth anomalies detected."
	}
	countByType := map[string]int{}
	for _, a := range anomalies {
		countByType[a.Type]++
	}
	return fmt.Sprintf(
		"Anomalies detected: acceleration_spike=%d trend_instability=%d sudden_growth=%d",
		countByType[anomalyAccelerationSpike],
		countByType[anomalyTrendInstability],
		countByType[anomalySuddenGrowth],
	)
}

func setCondition(plan *capacityv1.CapacityPlan, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&plan.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: plan.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *CapacityPlanReconciler) prometheusReadyStatus(spec capacityv1.CapacityPlanSpec) (metav1.ConditionStatus, string, string) {
	if strings.TrimSpace(spec.PrometheusURL) != "" {
		return metav1.ConditionTrue, "Configured", "Prometheus metrics client configured from CapacityPlan spec"
	}
	if _, ok := r.DefaultMetricsClient.(*metrics.PrometheusClient); ok {
		return metav1.ConditionTrue, "Configured", "Prometheus metrics client configured from operator startup settings"
	}
	return metav1.ConditionFalse, "Disabled", "Prometheus metrics client is disabled for this plan"
}

func (r *CapacityPlanReconciler) backfillReadyStatus() (metav1.ConditionStatus, string, string) {
	if !r.StartupBackfillConfigured {
		return metav1.ConditionFalse, "NotConfigured", "Startup backfill is disabled because --prometheus-url is not set"
	}
	if strings.TrimSpace(r.StartupBackfillError) == "" {
		return metav1.ConditionTrue, "Succeeded", fmt.Sprintf("Startup backfill completed successfully for %d PVCs", r.StartupBackfillSuccessfulPVCs)
	}
	return metav1.ConditionFalse, "PartialFailure", fmt.Sprintf("Startup backfill completed with errors after %d successful PVCs: %s", r.StartupBackfillSuccessfulPVCs, r.StartupBackfillError)
}

func toBrief(s capacityv1.PVCSummary) capacityv1.PVCSummaryBrief {
	return capacityv1.PVCSummaryBrief{
		Namespace:         s.Namespace,
		Name:              s.Name,
		UsageRatio:        s.UsageRatio,
		GrowthBytesPerDay: s.GrowthBytesPerDay,
		DaysUntilFull:     s.DaysUntilFull,
		AlertFiring:       s.AlertFiring,
	}
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
	riskDigest string,
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
	riskDigest = trimForAnnotation(riskDigest, maxRiskDigestAnnotation)

	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		map[string]interface{}{
			"name": "capacityplan.pvc",
			"rules": []interface{}{
				buildAlertRule("PVCUsageHigh",
					"capacityplan_pvc_usage_ratio > "+usageExpr,
					"5m", "warning", riskDigest),
				buildAlertRule("PVCUsageCritical",
					"capacityplan_pvc_usage_ratio > "+criticalExpr,
					"2m", "critical", riskDigest),
				buildAlertRule("PVCFillingUpSoon",
					"capacityplan_pvc_days_until_full < "+daysExpr+" and capacityplan_pvc_days_until_full >= 0",
					"10m", "warning", riskDigest),
				buildAlertRule("PVCFillingUpCritical",
					"capacityplan_pvc_days_until_full < "+criticalDaysExpr+" and capacityplan_pvc_days_until_full >= 0",
					"5m", "critical", riskDigest),
				buildAlertRule("PVCGrowthAccelerationSpike",
					`capacityplan_pvc_anomaly{type="acceleration_spike"} > 0`,
					"10m", "warning", riskDigest),
				buildAlertRule("PVCTrendInstability",
					`capacityplan_pvc_anomaly{type="trend_instability"} > 0`,
					"15m", "warning", riskDigest),
				buildAlertRuleWithSummary("NamespaceBudgetBreachSoon",
					"capacityplan_namespace_budget_days_to_breach >= 0 and capacityplan_namespace_budget_days_to_breach < 7",
					"10m", "warning",
					`Namespace budget breach soon on {{ $labels.namespace }}`,
					riskDigest),
				buildAlertRuleWithSummary("WorkloadBudgetBreachSoon",
					"capacityplan_workload_budget_days_to_breach >= 0 and capacityplan_workload_budget_days_to_breach < 7",
					"10m", "warning",
					`Workload budget breach soon on {{ $labels.namespace }}/{{ $labels.kind }}/{{ $labels.workload }}`,
					riskDigest),
			},
		},
	}, "spec", "groups")

	return obj
}

func buildAlertRule(alert, expr, forDur, severity, riskDigest string) map[string]interface{} {
	return buildAlertRuleWithSummary(
		alert,
		expr,
		forDur,
		severity,
		alert+" on {{ $labels.namespace }}/{{ $labels.pvc }}",
		riskDigest,
	)
}

func buildAlertRuleWithSummary(alert, expr, forDur, severity, summary, riskDigest string) map[string]interface{} {
	return map[string]interface{}{
		"alert": alert,
		"expr":  expr,
		"for":   forDur,
		"labels": map[string]interface{}{
			"severity": severity,
		},
		"annotations": map[string]interface{}{
			"summary":     summary,
			"description": "Capacity plan risk context: " + riskDigest,
		},
	}
}

func trimForAnnotation(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "No risk digest available."
	}
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
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
