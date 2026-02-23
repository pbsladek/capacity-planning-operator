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

// Package operator registers the custom Prometheus metrics exported by the
// capacity planning operator on the controller-runtime /metrics endpoint.
package operator

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// PVCUsageBytes is the current used storage in bytes for a PVC.
	// Set by PVCWatcherReconciler on every successful scrape.
	PVCUsageBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_usage_bytes",
			Help: "Current used storage in bytes for a PVC.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCCapacityBytes is the total allocated storage in bytes for a PVC.
	// Set by PVCWatcherReconciler on every successful scrape.
	PVCCapacityBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_capacity_bytes",
			Help: "Total allocated storage capacity in bytes for a PVC.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCUsageRatio is the ratio of used bytes to capacity (0.0–1.0) for a PVC.
	// Set by PVCWatcherReconciler on every successful scrape.
	PVCUsageRatio = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_usage_ratio",
			Help: "Ratio of used bytes to capacity (0.0–1.0) for a PVC.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCGrowthBytesPerDay is the OLS linear regression growth rate in bytes/day.
	// Set by CapacityPlanReconciler after each analysis cycle.
	PVCGrowthBytesPerDay = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_growth_bytes_per_day",
			Help: "Predicted storage growth rate in bytes per day for a PVC (OLS regression slope).",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCDaysUntilFull is the predicted number of days until the PVC is full.
	// -1 means not calculable (shrinking, flat, or insufficient data).
	// Set by CapacityPlanReconciler after each analysis cycle.
	PVCDaysUntilFull = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_days_until_full",
			Help: "Predicted days until the PVC reaches full capacity. -1 if not calculable.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCProjectedFullTimestampSeconds is the projected Unix timestamp at which
	// the PVC reaches full capacity, based on weekly growth trend.
	// -1 means not calculable.
	PVCProjectedFullTimestampSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_projected_full_timestamp_seconds",
			Help: "Projected Unix timestamp when a PVC reaches full capacity from weekly growth trend. -1 if not calculable.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCGrowthAcceleration captures short-term trend acceleration as:
	// (24h growth - weekly growth) / abs(weekly growth).
	PVCGrowthAcceleration = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_growth_acceleration",
			Help: "Relative acceleration of PVC growth trend based on daily vs weekly slope.",
		},
		[]string{"namespace", "pvc"},
	)

	// PVCRiskChangesTotal counts detected risk transitions per reconcile.
	PVCRiskChangesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capacityplan_pvc_risk_changes_total",
			Help: "Total detected PVC risk transitions by type (new, escalated, recovered).",
		},
		[]string{"type"},
	)

	// NamespaceBudgetDaysToBreach is the projected days until namespace budget breach.
	// -1 means not calculable or no positive growth.
	NamespaceBudgetDaysToBreach = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_namespace_budget_days_to_breach",
			Help: "Projected days until namespace storage budget breach. -1 if not calculable.",
		},
		[]string{"namespace"},
	)

	// WorkloadBudgetDaysToBreach is the projected days until workload budget breach.
	// -1 means not calculable or no positive growth.
	WorkloadBudgetDaysToBreach = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_workload_budget_days_to_breach",
			Help: "Projected days until workload storage budget breach. -1 if not calculable.",
		},
		[]string{"namespace", "kind", "workload"},
	)

	// PVCAnomaly indicates whether a PVC currently matches an anomaly type.
	// 1 means anomaly active, 0 means not active.
	PVCAnomaly = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_anomaly",
			Help: "Current anomaly flag per PVC and anomaly type (1 active, 0 inactive).",
		},
		[]string{"namespace", "pvc", "type"},
	)

	// PVCAnomaliesTotal counts detected PVC anomalies by type.
	PVCAnomaliesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capacityplan_pvc_anomalies_total",
			Help: "Total detected PVC anomalies by type.",
		},
		[]string{"type"},
	)

	// PVCSamplesCount is the number of samples currently held in the ring buffer.
	// Set by PVCWatcherReconciler after each push.
	PVCSamplesCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_samples_count",
			Help: "Number of samples currently stored in the ring buffer for a PVC.",
		},
		[]string{"namespace", "pvc"},
	)

	// LLMRequestsTotal counts LLM insight generation attempts.
	LLMRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capacityplan_llm_requests_total",
			Help: "Total LLM insight generation requests.",
		},
		[]string{"provider", "model"},
	)

	// LLMErrorsTotal counts failed LLM insight generation attempts.
	LLMErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capacityplan_llm_errors_total",
			Help: "Total failed LLM insight generation requests.",
		},
		[]string{"provider", "model"},
	)

	// LLMLatencySeconds observes LLM request latency.
	LLMLatencySeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "capacityplan_llm_latency_seconds",
			Help:    "Latency of LLM insight generation requests in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"provider", "model"},
	)
)

func init() {
	// Register with controller-runtime's shared registry so all metrics
	// appear on the single /metrics endpoint managed by the manager.
	metrics.Registry.MustRegister(
		PVCUsageBytes,
		PVCCapacityBytes,
		PVCUsageRatio,
		PVCGrowthBytesPerDay,
		PVCDaysUntilFull,
		PVCProjectedFullTimestampSeconds,
		PVCGrowthAcceleration,
		PVCRiskChangesTotal,
		NamespaceBudgetDaysToBreach,
		WorkloadBudgetDaysToBreach,
		PVCAnomaly,
		PVCAnomaliesTotal,
		PVCSamplesCount,
		LLMRequestsTotal,
		LLMErrorsTotal,
		LLMLatencySeconds,
	)
}
