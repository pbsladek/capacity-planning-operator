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

	// PrometheusURL is the base URL of the Prometheus instance used to query
	// kubelet_volume_stats_used_bytes (e.g. "http://prometheus:9090").
	// If empty, PVC usage metrics are not collected.
	// +optional
	PrometheusURL string `json:"prometheusURL,omitempty"`

	// Thresholds defines when alert conditions are triggered.
	// +optional
	Thresholds ThresholdSpec `json:"thresholds,omitempty"`

	// LLMInsightsInterval controls how often LLM insights are regenerated per PVC.
	// LLM calls are rate-limited by this interval; the reconciler skips calling
	// the LLM if the last insight is newer than this duration. Defaults to 6 hours.
	// +optional
	// +kubebuilder:default="6h"
	LLMInsightsInterval metav1.Duration `json:"llmInsightsInterval,omitempty"`

	// GrafanaDashboardNamespace is the namespace where the Grafana dashboard
	// ConfigMap is created. Defaults to the operator's own namespace.
	// +optional
	GrafanaDashboardNamespace string `json:"grafanaDashboardNamespace,omitempty"`
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

	// LastReconcileTime is when the CapacityPlan controller last ran successfully.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// ObservedGeneration is the generation of the CapacityPlan spec that produced this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
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
