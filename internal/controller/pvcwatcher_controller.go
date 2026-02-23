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
	stderrors "errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
	"github.com/pbsladek/capacity-planning-operator/internal/metrics"
	opmetrics "github.com/pbsladek/capacity-planning-operator/internal/operator"
)

// PVCState holds per-PVC in-memory state: the ring buffer of usage samples
// and the last known UID (to detect PVC deletion/recreation).
type PVCState struct {
	Buffer  *analysis.RingBuffer
	LastUID types.UID
}

// PVCWatcherReconciler watches PersistentVolumeClaims across all namespaces,
// queries their current usage from Prometheus, pushes samples into ring buffers,
// and updates the operator's Prometheus metrics. It is the data-collection half
// of the capacity planning operator — it never writes to etcd.
//
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
type PVCWatcherReconciler struct {
	client.Client

	mu            sync.RWMutex
	metricsClient metrics.PVCMetricsClient
	pvcStates     map[string]*PVCState // key: "namespace/name"
	retention     int                  // ring buffer capacity (samples)
}

// NewPVCWatcherReconciler creates a reconciler with the given ring-buffer capacity.
// capacity must be >= 1; see CapacityPlanSpec.SampleRetention.
func NewPVCWatcherReconciler(c client.Client, mc metrics.PVCMetricsClient, capacity int) *PVCWatcherReconciler {
	if capacity < 1 {
		capacity = 720
	}
	return &PVCWatcherReconciler{
		Client:        c,
		metricsClient: mc,
		pvcStates:     make(map[string]*PVCState),
		retention:     capacity,
	}
}

// Reconcile is triggered whenever a PVC is created, updated, or deleted.
// It queries the PVC's current usage from Prometheus and records a sample.
func (r *PVCWatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("pvc", req.Namespace+"/"+req.Name)
	key := req.Namespace + "/" + req.Name

	// Fetch the PVC.
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, req.NamespacedName, &pvc); err != nil {
		if errors.IsNotFound(err) {
			// PVC deleted — clean up its state.
			r.deleteState(key)
			logger.V(1).Info("PVC deleted; cleaned in-memory state")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Ensure state exists for this PVC; reset on UID change (re-created PVC).
	state := r.ensureState(key, pvc.UID)

	// Query current usage from Prometheus. Errors here are non-fatal:
	// we log a warning and skip recording a sample rather than failing the
	// reconcile and triggering backoff, since Prometheus may be temporarily
	// unavailable.
	mc := r.getMetricsClient()
	usage, err := mc.GetUsage(ctx, metrics.PVCKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	})
	if err != nil {
		logger.V(1).Info("failed to get PVC usage from metrics client",
			"error", err)
		return ctrl.Result{}, nil
	}

	// Record the sample.
	state.Buffer.Push(analysis.Sample{
		Timestamp: time.Now(),
		UsedBytes: usage.UsedBytes,
	})

	// Determine capacity: prefer Prometheus value, fall back to PVC spec.
	capacityBytes := usage.CapacityBytes
	if capacityBytes == 0 {
		if qty, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			capacityBytes = qty.Value()
		}
	}

	// Update operator Prometheus metrics.
	labels := []string{req.Namespace, req.Name}
	opmetrics.PVCUsageBytes.WithLabelValues(labels...).Set(float64(usage.UsedBytes))
	opmetrics.PVCCapacityBytes.WithLabelValues(labels...).Set(float64(capacityBytes))
	opmetrics.PVCSamplesCount.WithLabelValues(labels...).Set(float64(state.Buffer.Len()))

	if capacityBytes > 0 {
		ratio := float64(usage.UsedBytes) / float64(capacityBytes)
		opmetrics.PVCUsageRatio.WithLabelValues(labels...).Set(ratio)
	}
	logger.V(1).Info("recorded PVC usage sample",
		"usedBytes", usage.UsedBytes,
		"capacityBytes", capacityBytes,
		"samplesCount", state.Buffer.Len(),
	)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the controller-runtime manager.
func (r *PVCWatcherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolumeClaim{}).
		Named("pvc-watcher").
		Complete(r)
}

// GetSnapshot returns a chronological copy of the ring-buffer samples for the
// given "namespace/name" key, or nil if no state exists.
// Safe for concurrent use; intended for CapacityPlanReconciler to read.
func (r *PVCWatcherReconciler) GetSnapshot(key string) []analysis.Sample {
	r.mu.RLock()
	state, ok := r.pvcStates[key]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	return state.Buffer.Snapshot()
}

// BackfillFromRange populates the ring buffer for a PVC with historical data
// retrieved from the metrics backend (Prometheus query_range). Called during
// operator startup so that predictions are available before the first scrape.
func (r *PVCWatcherReconciler) BackfillFromRange(
	ctx context.Context,
	namespace, name string,
	uid types.UID,
	start, end time.Time,
	step time.Duration,
) error {
	logger := log.FromContext(ctx).WithValues("pvc", namespace+"/"+name)
	key := namespace + "/" + name
	state := r.ensureState(key, uid)

	mc := r.getMetricsClient()
	points, err := mc.GetUsageRange(ctx, metrics.PVCKey{
		Namespace: namespace,
		Name:      name,
	}, start, end, step)
	if err != nil {
		return err
	}
	logger.V(1).Info("backfilling PVC from metrics range",
		"start", start.UTC().Format(time.RFC3339),
		"end", end.UTC().Format(time.RFC3339),
		"step", step.String(),
		"points", len(points),
	)

	for _, p := range points {
		state.Buffer.Push(analysis.Sample{
			Timestamp: p.Timestamp,
			UsedBytes: p.UsedBytes,
		})
	}
	return nil
}

// BackfillAllPVCs backfills in-memory ring buffers for all PVCs currently
// visible to the operator client. Errors are aggregated; successful PVCs are
// still backfilled even if others fail.
func (r *PVCWatcherReconciler) BackfillAllPVCs(
	ctx context.Context,
	start, end time.Time,
	step time.Duration,
) (int, error) {
	logger := log.FromContext(ctx)
	if step <= 0 {
		return 0, fmt.Errorf("step must be > 0")
	}

	var list corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &list); err != nil {
		return 0, err
	}

	success := 0
	var errs []error
	for i := range list.Items {
		pvc := &list.Items[i]
		if err := r.BackfillFromRange(ctx, pvc.Namespace, pvc.Name, pvc.UID, start, end, step); err != nil {
			logger.V(1).Info("PVC backfill failed", "pvc", pvc.Namespace+"/"+pvc.Name, "error", err)
			errs = append(errs, fmt.Errorf("%s/%s: %w", pvc.Namespace, pvc.Name, err))
			continue
		}
		success++
	}

	logger.V(1).Info("PVC backfill completed",
		"successfulPVCs", success,
		"totalPVCs", len(list.Items),
		"hadErrors", len(errs) > 0,
	)
	return success, stderrors.Join(errs...)
}

// AllKeys returns the set of PVC keys currently tracked, as "namespace/name" strings.
// Used by CapacityPlanReconciler to enumerate all known PVCs.
func (r *PVCWatcherReconciler) AllKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.pvcStates))
	for k := range r.pvcStates {
		keys = append(keys, k)
	}
	return keys
}

// ensureState returns the PVCState for the given key, creating it if missing.
// If the UID has changed (PVC was deleted and recreated), the ring buffer is reset.
func (r *PVCWatcherReconciler) ensureState(key string, uid types.UID) *PVCState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.pvcStates[key]
	if !ok {
		state = &PVCState{
			Buffer:  analysis.NewRingBuffer(r.retention),
			LastUID: uid,
		}
		r.pvcStates[key] = state
		return state
	}
	if state.LastUID != uid {
		state.Buffer.Reset()
		state.LastUID = uid
	}
	return state
}

// Configure updates the watcher's active metrics client and ring-buffer
// retention. Existing buffers are resized to honor the new retention.
func (r *PVCWatcherReconciler) Configure(mc metrics.PVCMetricsClient, retention int) {
	if retention < 1 {
		retention = 720
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if mc != nil {
		r.metricsClient = mc
	}
	if retention == r.retention {
		return
	}

	r.retention = retention
	for _, state := range r.pvcStates {
		snapshot := state.Buffer.Snapshot()
		if len(snapshot) > retention {
			snapshot = snapshot[len(snapshot)-retention:]
		}
		resized := analysis.NewRingBuffer(retention)
		for _, s := range snapshot {
			resized.Push(s)
		}
		state.Buffer = resized
	}
}

func (r *PVCWatcherReconciler) getMetricsClient() metrics.PVCMetricsClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.metricsClient == nil {
		return &metrics.MockPVCMetricsClient{}
	}
	return r.metricsClient
}

// deleteState removes the PVC's in-memory state and deletes its Prometheus metric
// series so stale data is not exported after a PVC is deleted.
func (r *PVCWatcherReconciler) deleteState(key string) {
	r.mu.Lock()
	_, ok := r.pvcStates[key]
	if ok {
		delete(r.pvcStates, key)
	}
	r.mu.Unlock()

	if !ok {
		return
	}

	// Parse namespace/name to delete labelled metric series.
	ns, name := splitKey(key)
	labels := []string{ns, name}
	opmetrics.PVCUsageBytes.DeleteLabelValues(labels...)
	opmetrics.PVCCapacityBytes.DeleteLabelValues(labels...)
	opmetrics.PVCUsageRatio.DeleteLabelValues(labels...)
	opmetrics.PVCSamplesCount.DeleteLabelValues(labels...)
	opmetrics.PVCGrowthBytesPerDay.DeleteLabelValues(labels...)
	opmetrics.PVCDaysUntilFull.DeleteLabelValues(labels...)
	opmetrics.PVCProjectedFullTimestampSeconds.DeleteLabelValues(labels...)
	opmetrics.PVCGrowthAcceleration.DeleteLabelValues(labels...)
	for _, t := range supportedAnomalyTypes {
		opmetrics.PVCAnomaly.DeleteLabelValues(ns, name, t)
	}
}

// splitKey splits a "namespace/name" key into its components.
func splitKey(key string) (namespace, name string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}
