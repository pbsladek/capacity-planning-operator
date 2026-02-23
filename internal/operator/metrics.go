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

	// PVCSamplesCount is the number of samples currently held in the ring buffer.
	// Set by PVCWatcherReconciler after each push.
	PVCSamplesCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capacityplan_pvc_samples_count",
			Help: "Number of samples currently stored in the ring buffer for a PVC.",
		},
		[]string{"namespace", "pvc"},
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
		PVCSamplesCount,
	)
}
