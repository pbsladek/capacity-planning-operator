/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CapacityPlanSpec defines the desired configuration for the capacity planning operator.
type CapacityPlanSpec struct {
	// Namespaces is the list of namespaces to watch for PVCs.
	// An empty list means watch all namespaces.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// SampleRetention is the maximum number of samples to keep per PVC in the
	// in-memory ring buffer. At a 5-minute scrape interval, 720 samples covers 60 hours.
	// Defaults to 720.
	// +optional
	// +kubebuilder:default=720
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8760
	SampleRetention int `json:"sampleRetention,omitempty"`

	// ReconcileInterval controls how often the CapacityPlan reconcile loop runs
	// to compute growth rates and update status. Defaults to 1 hour.
	// +optional
	// +kubebuilder:default="1h"
	ReconcileInterval metav1.Duration `json:"reconcileInterval,omitempty"`

	// SampleInterval controls how often each PVC is sampled by the watcher.
	// Defaults to 30 seconds.
	// +optional
	// +kubebuilder:default="30s"
	SampleInterval metav1.Duration `json:"sampleInterval,omitempty"`

	// PrometheusURL is the base URL of the Prometheus instance used to query
	// kubelet_volume_stats_used_bytes (e.g. "http://prometheus:9090").
	// If empty, PVC usage metrics are not collected.
	// +optional
	PrometheusURL string `json:"prometheusURL,omitempty"`

	// Thresholds defines when alert conditions are triggered.
	// +optional
	Thresholds ThresholdSpec `json:"thresholds,omitempty"`

	// Budgets configures optional namespace/workload storage budget forecasting.
	// +optional
	Budgets BudgetSpec `json:"budgets,omitempty"`

	// LLMInsightsInterval controls how often LLM insights are regenerated per PVC.
	// LLM calls are rate-limited by this interval; the reconciler skips calling
	// the LLM if the last insight is newer than this duration. Defaults to 6 hours.
	// +optional
	// +kubebuilder:default="6h"
	LLMInsightsInterval metav1.Duration `json:"llmInsightsInterval,omitempty"`

	// LLM configures how insights are generated (provider/model/credentials).
	// When provider is "disabled" (default), no new LLM insights are generated.
	// +optional
	LLM LLMProviderSpec `json:"llm,omitempty"`

	// GrafanaDashboardNamespace is the namespace where the Grafana dashboard
	// ConfigMap is created. Defaults to the operator's own namespace.
	// +optional
	GrafanaDashboardNamespace string `json:"grafanaDashboardNamespace,omitempty"`
}

// LLMProviderSpec configures the insight generation backend.
type LLMProviderSpec struct {
	// Provider selects the backend implementation.
	// +optional
	// +kubebuilder:default="disabled"
	// +kubebuilder:validation:Enum=disabled;openai;anthropic;fastapi
	Provider string `json:"provider,omitempty"`

	// Model is the model identifier used by OpenAI/Anthropic/FastAPI backends.
	// +optional
	Model string `json:"model,omitempty"`

	// Timeout controls request timeout for LLM calls. Defaults to 15s.
	// +optional
	// +kubebuilder:default="15s"
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// MaxTokens limits response size. Defaults to 256.
	// +optional
	// +kubebuilder:default=256
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8192
	MaxTokens int `json:"maxTokens,omitempty"`

	// Temperature controls sampling randomness. Nil uses provider defaults.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2
	Temperature *float64 `json:"temperature,omitempty"`

	// OnlyAlertingPVCs limits LLM refreshes to PVCs currently in alert state.
	// When true, non-alerting PVCs keep their previous insight unchanged.
	// Defaults to false.
	// +optional
	// +kubebuilder:default=false
	OnlyAlertingPVCs bool `json:"onlyAlertingPVCs,omitempty"`

	// OpenAI provider settings.
	// +optional
	OpenAI OpenAIProviderSpec `json:"openai,omitempty"`

	// Anthropic provider settings.
	// +optional
	Anthropic AnthropicProviderSpec `json:"anthropic,omitempty"`

	// FastAPI provider settings.
	// +optional
	FastAPI FastAPIProviderSpec `json:"fastapi,omitempty"`
}

// OpenAIProviderSpec contains OpenAI backend settings.
type OpenAIProviderSpec struct {
	// SecretRefName is the Secret name (in operator namespace) holding API key.
	// +optional
	SecretRefName string `json:"secretRefName,omitempty"`

	// SecretKey is the data key inside SecretRefName. Defaults to "apiKey".
	// +optional
	// +kubebuilder:default="apiKey"
	SecretKey string `json:"secretKey,omitempty"`

	// BaseURL overrides the OpenAI API base URL.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
}

// AnthropicProviderSpec contains Anthropic backend settings.
type AnthropicProviderSpec struct {
	// SecretRefName is the Secret name (in operator namespace) holding API key.
	// +optional
	SecretRefName string `json:"secretRefName,omitempty"`

	// SecretKey is the data key inside SecretRefName. Defaults to "apiKey".
	// +optional
	// +kubebuilder:default="apiKey"
	SecretKey string `json:"secretKey,omitempty"`

	// BaseURL overrides the Anthropic API base URL.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
}

// FastAPIProviderSpec contains in-cluster FastAPI backend settings.
type FastAPIProviderSpec struct {
	// URL is the FastAPI endpoint for insight generation.
	// Example: http://llm-api.ml.svc.cluster.local:8000/v1/insights
	// +optional
	URL string `json:"url,omitempty"`

	// AuthSecretRefName is an optional Secret name (operator namespace)
	// containing a bearer token for FastAPI requests.
	// +optional
	AuthSecretRefName string `json:"authSecretRefName,omitempty"`

	// AuthSecretKey is the token key in AuthSecretRefName. Defaults to "token".
	// +optional
	// +kubebuilder:default="token"
	AuthSecretKey string `json:"authSecretKey,omitempty"`

	// TLSSkipVerify disables TLS cert verification for FastAPI HTTPS endpoint.
	// Prefer false in production. Defaults to false.
	// +optional
	TLSSkipVerify bool `json:"tlsSkipVerify,omitempty"`

	// HealthURL is an optional health endpoint used for degraded-mode checks.
	// If empty, the client derives one as <scheme>://<host>/healthz.
	// +optional
	HealthURL string `json:"healthURL,omitempty"`

	// FailureThreshold is the number of consecutive failures before entering
	// degraded mode. Defaults to 3.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	FailureThreshold int `json:"failureThreshold,omitempty"`

	// Cooldown controls how long degraded mode lasts before trying a health check
	// again. Defaults to 1 minute.
	// +optional
	// +kubebuilder:default="1m"
	Cooldown metav1.Duration `json:"cooldown,omitempty"`
}

// ThresholdSpec defines usage thresholds that trigger alert conditions.
type ThresholdSpec struct {
	// UsageRatio is the used/capacity ratio (0.0-1.0) at which an alert fires.
	// Stored as a string to avoid float JSON serialization issues. Default "0.85".
	// +optional
	// +kubebuilder:default="0.85"
	// +kubebuilder:validation:Pattern=`^(0(\.\d+)?|1(\.0+)?)$`
	UsageRatio string `json:"usageRatio,omitempty"`

	// DaysUntilFull triggers an alert when the predicted days until full falls
	// below this value. Set to 0 to disable this check. Default 7.
	// +optional
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=0
	DaysUntilFull int `json:"daysUntilFull,omitempty"`
}

// BudgetSpec configures namespace/workload storage budgets for forecast reporting.
type BudgetSpec struct {
	// NamespaceBudgets defines per-namespace storage budgets.
	// +optional
	NamespaceBudgets []NamespaceBudgetSpec `json:"namespaceBudgets,omitempty"`

	// WorkloadBudgets defines per-workload storage budgets.
	// +optional
	WorkloadBudgets []WorkloadBudgetSpec `json:"workloadBudgets,omitempty"`
}

// NamespaceBudgetSpec defines a storage budget for a namespace.
type NamespaceBudgetSpec struct {
	Namespace string `json:"namespace"`
	// Budget is a Kubernetes quantity string (for example "20Ti").
	Budget string `json:"budget"`
}

// WorkloadBudgetSpec defines a storage budget for a specific workload.
type WorkloadBudgetSpec struct {
	Namespace string `json:"namespace"`
	// Kind is workload kind (for example StatefulSet, Deployment, CronJob).
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Budget is a Kubernetes quantity string (for example "5Ti").
	Budget string `json:"budget"`
}

// PVCSummary holds the latest computed state for a single PVC.
// Raw samples are never written to etcd — they live exclusively in the
// in-memory ring buffer inside PVCWatcherReconciler.
type PVCSummary struct {
	// Namespace is the namespace of the PVC.
	Namespace string `json:"namespace"`

	// Name is the name of the PVC.
	Name string `json:"name"`

	// CapacityBytes is the total allocated storage in bytes, from pvc.spec.resources.requests.storage.
	CapacityBytes int64 `json:"capacityBytes"`

	// UsedBytes is the most recently observed used storage in bytes.
	UsedBytes int64 `json:"usedBytes"`

	// UsageRatio is UsedBytes/CapacityBytes as a float in [0.0, 1.0].
	UsageRatio float64 `json:"usageRatio"`

	// GrowthBytesPerDay is the OLS linear regression slope over the sample window,
	// in bytes per day. Negative means the volume is shrinking.
	GrowthBytesPerDay float64 `json:"growthBytesPerDay"`

	// DaysUntilFull is the predicted number of days until the PVC reaches 100% capacity.
	// Nil if growth rate is <= 0 (shrinking or flat) or capacity is unknown.
	// +optional
	DaysUntilFull *float64 `json:"daysUntilFull,omitempty"`

	// ConfidenceR2 is the R-squared coefficient of determination for the growth
	// regression (0.0-1.0). Values below 0.5 indicate an unreliable trend.
	ConfidenceR2 float64 `json:"confidenceR2"`

	// SamplesCount is the number of samples currently held in the ring buffer for this PVC.
	SamplesCount int `json:"samplesCount"`

	// LastSampleTime is when the most recent sample was recorded.
	// +optional
	LastSampleTime *metav1.Time `json:"lastSampleTime,omitempty"`

	// LLMInsight is the most recent LLM-generated analysis for this PVC.
	// Empty if LLM is not configured or has not run yet.
	// +optional
	LLMInsight string `json:"llmInsight,omitempty"`

	// LastLLMTime is when the LLMInsight was last generated.
	// +optional
	LastLLMTime *metav1.Time `json:"lastLLMTime,omitempty"`

	// AlertFiring is true when this PVC has crossed a configured alert threshold.
	AlertFiring bool `json:"alertFiring"`
}

// CapacityPlanStatus defines the observed state of CapacityPlan.
type CapacityPlanStatus struct {
	// Conditions hold the latest available observations of the CapacityPlan's state.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// PVCs holds per-PVC summaries for all tracked PersistentVolumeClaims.
	// +optional
	PVCs []PVCSummary `json:"pvcs,omitempty"`

	// Summary is a compact leaderboard and count overview for quick operator UX.
	// +optional
	Summary CapacityPlanSummary `json:"summary,omitempty"`

	// TopRisks lists the highest-risk PVCs ranked by weekly growth trend.
	// +optional
	TopRisks []PVCRiskSummary `json:"topRisks,omitempty"`

	// RiskDigest is a human-readable summary of top growth and projected fill dates.
	// +optional
	RiskDigest string `json:"riskDigest,omitempty"`

	// RiskChanges captures new/escalated/recovered risk transitions for this reconcile.
	// +optional
	RiskChanges []PVCRiskChange `json:"riskChanges,omitempty"`

	// RiskChangeSummary is a compact textual summary of RiskChanges.
	// +optional
	RiskChangeSummary string `json:"riskChangeSummary,omitempty"`

	// RiskSnapshotHash is a digest of the current TopRisks set for change detection.
	// +optional
	RiskSnapshotHash string `json:"riskSnapshotHash,omitempty"`

	// NamespaceForecasts contains namespace-level budget forecasts.
	// +optional
	NamespaceForecasts []StorageBudgetForecast `json:"namespaceForecasts,omitempty"`

	// WorkloadForecasts contains workload-level budget forecasts.
	// +optional
	WorkloadForecasts []StorageBudgetForecast `json:"workloadForecasts,omitempty"`

	// Anomalies contains detected growth-behavior anomalies.
	// +optional
	Anomalies []PVCAnomaly `json:"anomalies,omitempty"`

	// AnomalySummary is a compact textual summary of anomalies.
	// +optional
	AnomalySummary string `json:"anomalySummary,omitempty"`

	// LastReconcileTime is when the CapacityPlan controller last ran successfully.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// ObservedGeneration is the generation of the CapacityPlan spec that produced this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// CapacityPlanSummary is a compact status overview.
type CapacityPlanSummary struct {
	// TotalPVCs is the number of PVC summaries included in status.
	TotalPVCs int `json:"totalPVCs"`

	// AlertingPVCs is the number of PVCs where AlertFiring=true.
	AlertingPVCs int `json:"alertingPVCs"`

	// TopByUsage lists the highest usage-ratio PVCs.
	// +optional
	TopByUsage []PVCSummaryBrief `json:"topByUsage,omitempty"`

	// TopBySoonestToFull lists PVCs with the lowest non-nil DaysUntilFull.
	// +optional
	TopBySoonestToFull []PVCSummaryBrief `json:"topBySoonestToFull,omitempty"`

	// TopByGrowth lists PVCs with the highest positive growth rate.
	// +optional
	TopByGrowth []PVCSummaryBrief `json:"topByGrowth,omitempty"`
}

// PVCSummaryBrief is a compact per-PVC summary for leaderboard views.
type PVCSummaryBrief struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	UsageRatio        float64  `json:"usageRatio"`
	GrowthBytesPerDay float64  `json:"growthBytesPerDay"`
	DaysUntilFull     *float64 `json:"daysUntilFull,omitempty"`
	AlertFiring       bool     `json:"alertFiring"`
}

// PVCRiskSummary is a trend-focused risk view for a single PVC.
type PVCRiskSummary struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	WorkloadKind      string `json:"workloadKind,omitempty"`
	WorkloadName      string `json:"workloadName,omitempty"`
	WorkloadNamespace string `json:"workloadNamespace,omitempty"`

	UsedBytes               int64        `json:"usedBytes"`
	CapacityBytes           int64        `json:"capacityBytes"`
	UsageRatio              float64      `json:"usageRatio"`
	WeeklyGrowthBytesPerDay float64      `json:"weeklyGrowthBytesPerDay"`
	DailyGrowthBytesPerDay  float64      `json:"dailyGrowthBytesPerDay"`
	GrowthAcceleration      float64      `json:"growthAcceleration"`
	ConfidenceR2            float64      `json:"confidenceR2"`
	DaysUntilFull           *float64     `json:"daysUntilFull,omitempty"`
	ProjectedFullAt         *metav1.Time `json:"projectedFullAt,omitempty"`
	LLMInsight              string       `json:"llmInsight,omitempty"`
	AlertFiring             bool         `json:"alertFiring"`
}

// PVCRiskChange describes a risk transition between previous and current top risks.
type PVCRiskChange struct {
	// Type is one of: new, escalated, recovered.
	Type string `json:"type"`

	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	PreviousWeeklyGrowthBytesPerDay float64 `json:"previousWeeklyGrowthBytesPerDay,omitempty"`
	CurrentWeeklyGrowthBytesPerDay  float64 `json:"currentWeeklyGrowthBytesPerDay,omitempty"`

	// +optional
	PreviousProjectedFullAt *metav1.Time `json:"previousProjectedFullAt,omitempty"`
	// +optional
	CurrentProjectedFullAt *metav1.Time `json:"currentProjectedFullAt,omitempty"`

	Message string      `json:"message"`
	Time    metav1.Time `json:"time"`
}

// StorageBudgetForecast describes projected budget breach timing for a namespace or workload.
type StorageBudgetForecast struct {
	Scope string `json:"scope"` // namespace|workload

	Namespace string `json:"namespace"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`

	BudgetBytes       int64   `json:"budgetBytes"`
	UsedBytes         int64   `json:"usedBytes"`
	UsageRatio        float64 `json:"usageRatio"`
	GrowthBytesPerDay float64 `json:"growthBytesPerDay"`

	// +optional
	DaysUntilBreach *float64 `json:"daysUntilBreach,omitempty"`
	// +optional
	ProjectedBreachAt *metav1.Time `json:"projectedBreachAt,omitempty"`
}

// PVCAnomaly describes an anomalous growth behavior detected for a PVC.
type PVCAnomaly struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	Type     string      `json:"type"`     // acceleration_spike|trend_instability|sudden_growth
	Severity string      `json:"severity"` // warning|critical
	Message  string      `json:"message"`
	Time     metav1.Time `json:"time"`

	WorkloadKind string `json:"workloadKind,omitempty"`
	WorkloadName string `json:"workloadName,omitempty"`

	WeeklyGrowthBytesPerDay float64 `json:"weeklyGrowthBytesPerDay"`
	DailyGrowthBytesPerDay  float64 `json:"dailyGrowthBytesPerDay"`
	GrowthAcceleration      float64 `json:"growthAcceleration"`
	ConfidenceR2            float64 `json:"confidenceR2"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=capacityplans,scope=Cluster,shortName=cp
// +kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.reconcileInterval`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Last Reconcile",type="date",JSONPath=".status.lastReconcileTime"

// CapacityPlan is the cluster-scoped configuration object for the capacity planning operator.
// It controls which namespaces and resource types are watched, how data is retained,
// and where alerts and dashboards are published.
type CapacityPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CapacityPlanSpec   `json:"spec,omitempty"`
	Status CapacityPlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CapacityPlanList contains a list of CapacityPlan.
type CapacityPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityPlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CapacityPlan{}, &CapacityPlanList{})
}
