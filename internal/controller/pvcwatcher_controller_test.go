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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgmetrics "github.com/pbsladek/capacity-planning-operator/internal/metrics"
)

var _ = Describe("PVCWatcher controller", func() {
	const testNS = "default"

	// makePVC creates a minimal PVC in the cluster and returns it.
	makePVC := func(name string, storageReq string) *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNS,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(storageReq),
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
		return pvc
	}

	BeforeEach(func() {
		// Reset mock state before each test.
		mockMetricsClient.Data = make(map[string]pkgmetrics.PVCUsage)
		mockMetricsClient.Err = nil
	})

	Describe("PVC creation triggers reconcile and records a sample", func() {
		It("pushes one sample into the ring buffer after a PVC is created", func() {
			name := "test-pvc-sample"
			key := testNS + "/" + name

			// Pre-configure the mock so the reconcile finds usage data.
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     2 * 1024 * 1024 * 1024, // 2 GiB
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}

			makePVC(name, "10Gi")

			// The reconcile loop triggers asynchronously; poll until the buffer
			// has data or the test times out.
			Eventually(func() int {
				snap := pvcWatcher.GetSnapshot(key)
				return len(snap)
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			snap := pvcWatcher.GetSnapshot(key)
			Expect(snap[0].UsedBytes).To(Equal(int64(2 * 1024 * 1024 * 1024)))
		})
	})

	Describe("Metrics are exported", func() {
		It("does not error when mock returns 5GB used", func() {
			name := "test-pvc-metrics"
			key := testNS + "/" + name

			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     5 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}
			makePVC(name, "10Gi")

			// Just verify no panic and the sample is recorded.
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))
		})
	})

	Describe("Metrics client error is non-fatal", func() {
		It("returns nil from reconcile when mock returns an error", func() {
			name := "test-pvc-err"
			key := testNS + "/" + name

			// Force an error on all GetUsage calls.
			mockMetricsClient.Err = fmt.Errorf("prometheus unavailable")
			makePVC(name, "10Gi")

			// The reconciler should not panic or enqueue a retried error,
			// and the ring buffer should remain empty.
			Consistently(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "2s", "200ms").Should(Equal(0))

			// Unblock subsequent tests.
			mockMetricsClient.Err = nil
		})
	})

	Describe("PVC deletion removes state", func() {
		It("clears the ring buffer when the PVC is deleted", func() {
			name := "test-pvc-delete"
			key := testNS + "/" + name

			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     1 * 1024 * 1024 * 1024,
				CapacityBytes: 5 * 1024 * 1024 * 1024,
			}
			pvc := makePVC(name, "5Gi")

			// Wait for the sample to arrive.
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// Remove finalizers so the PVC can be garbage-collected in envtest
			// (the pvc-protection controller does not run in envtest).
			pvcPatch := pvc.DeepCopy()
			pvcPatch.Finalizers = nil
			Expect(k8sClient.Patch(ctx, pvcPatch, client.MergeFrom(pvc))).To(Succeed())

			// Now delete the PVC.
			Expect(k8sClient.Delete(ctx, pvcPatch)).To(Succeed())

			// Wait for the object to disappear from the API.
			Eventually(func() bool {
				var found corev1.PersistentVolumeClaim
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNS, Name: name}, &found)
				return err != nil // true when not found
			}, "10s", "200ms").Should(BeTrue())

			// Ring buffer state should be cleaned up after the watcher reconciles the deletion.
			Eventually(func() bool {
				return pvcWatcher.GetSnapshot(key) == nil
			}, "5s", "200ms").Should(BeTrue())
		})
	})
})
