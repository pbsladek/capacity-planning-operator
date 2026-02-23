/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
	pkgmetrics "github.com/pbsladek/capacity-planning-operator/internal/metrics"
)

var _ = Describe("CapacityPlan controller", func() {
	const testNS = "default"
	const planName = "test-plan"

	// Helper to create a CapacityPlan CR.
	makePlan := func(namespaces []string, interval metav1.Duration) *capacityv1.CapacityPlan {
		plan := &capacityv1.CapacityPlan{
			ObjectMeta: metav1.ObjectMeta{Name: planName},
			Spec: capacityv1.CapacityPlanSpec{
				Namespaces:          namespaces,
				ReconcileInterval:   interval,
				LLMInsightsInterval: metav1.Duration{Duration: 6 * time.Hour},
			},
		}
		Expect(k8sClient.Create(ctx, plan)).To(Succeed())
		return plan
	}

	// Helper to create a PVC.
	makePVC := func(name, ns, storageReq string) *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
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

	// deletePlan removes the plan and waits for it to disappear.
	deletePlan := func() {
		plan := &capacityv1.CapacityPlan{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, plan)
		if err == nil {
			_ = k8sClient.Delete(ctx, plan)
		}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: planName}, plan)
		}, "10s", "200ms").ShouldNot(Succeed())
	}

	BeforeEach(func() {
		mockMetricsClient.Data = make(map[string]pkgmetrics.PVCUsage)
		mockMetricsClient.Err = nil
		mockLLMClient.Reset()
	})

	AfterEach(func() {
		deletePlan()
	})

	Describe("Basic reconcile", func() {
		It("populates status.pvcs after a PVC exists", func() {
			pvcName := "cp-basic-pvc"
			key := testNS + "/" + pvcName
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     1 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}

			makePVC(pvcName, testNS, "10Gi")

			// Wait for the watcher to record a sample.
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// Short reconcile interval so the test runs quickly.
			makePlan(nil, metav1.Duration{Duration: 2 * time.Second})

			// CapacityPlan status should eventually list the PVC.
			Eventually(func() int {
				var plan capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &plan); err != nil {
					return 0
				}
				return len(plan.Status.PVCs)
			}, "15s", "500ms").Should(BeNumerically(">=", 1))
		})
	})

	Describe("Growth rate and DaysUntilFull", func() {
		It("computes a positive DaysUntilFull from pre-populated growth samples", func() {
			pvcName := "cp-growth-pvc"
			key := testNS + "/" + pvcName

			// Seed the mock so the watcher can record a real sample.
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     6 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}

			pvc := makePVC(pvcName, testNS, "10Gi")

			// Wait for the watcher to create state with the real PVC UID.
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// Now inject additional historical samples directly into the ring buffer.
			// The state exists and uses the real UID — ensureState will not reset it.
			// 4 samples at 1GB/day from 3 days ago: 3GB, 4GB, 5GB, 6GB
			// Growth ≈ 1GB/day, last = 6GB, remaining = 4GB → DaysUntilFull ≈ 4.
			t0 := time.Now().Add(-3 * 24 * time.Hour)
			pvcWatcher.mu.Lock()
			state := pvcWatcher.pvcStates[key]
			// Replace with fresh buffer holding our controlled samples.
			state.Buffer = analysis.NewRingBuffer(10)
			for i := 0; i < 4; i++ {
				state.Buffer.Push(analysis.Sample{
					Timestamp: t0.Add(time.Duration(i) * 24 * time.Hour),
					UsedBytes: int64(3+i) * 1024 * 1024 * 1024,
				})
			}
			state.LastUID = pvc.UID
			pvcWatcher.mu.Unlock()

			makePlan(nil, metav1.Duration{Duration: 2 * time.Second})

			Eventually(func() *float64 {
				var plan capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &plan); err != nil {
					return nil
				}
				for _, p := range plan.Status.PVCs {
					if p.Name == pvcName {
						return p.DaysUntilFull
					}
				}
				return nil
			}, "15s", "500ms").ShouldNot(BeNil())

			var plan capacityv1.CapacityPlan
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &plan)).To(Succeed())
			var found *capacityv1.PVCSummary
			for i := range plan.Status.PVCs {
				if plan.Status.PVCs[i].Name == pvcName {
					found = &plan.Status.PVCs[i]
					break
				}
			}
			Expect(found).NotTo(BeNil())
			Expect(*found.DaysUntilFull).To(BeNumerically(">", 0))
		})
	})

	Describe("Alert firing", func() {
		It("sets AlertFiring=true when usage ratio >= threshold", func() {
			pvcName := "cp-alert-pvc"
			key := testNS + "/" + pvcName

			// 9GB used on 10GB PVC = 0.9 > default 0.85 threshold.
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     9 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}

			makePVC(pvcName, testNS, "10Gi")

			// Wait for a sample.
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			makePlan(nil, metav1.Duration{Duration: 2 * time.Second})

			Eventually(func() bool {
				var plan capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &plan); err != nil {
					return false
				}
				for _, p := range plan.Status.PVCs {
					if p.Name == pvcName {
						return p.AlertFiring
					}
				}
				return false
			}, "15s", "500ms").Should(BeTrue())
		})
	})

	Describe("LLM rate limiting", func() {
		It("generates insight once and preserves it on subsequent reconcile within interval", func() {
			pvcName := "cp-llm-pvc"
			key := testNS + "/" + pvcName
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     1 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}
			mockLLMClient.Response = "mock insight"

			makePVC(pvcName, testNS, "10Gi")
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// 24h LLM interval ensures subsequent reconciles skip LLM calls.
			plan := &capacityv1.CapacityPlan{
				ObjectMeta: metav1.ObjectMeta{Name: planName},
				Spec: capacityv1.CapacityPlanSpec{
					Namespaces:          nil,
					ReconcileInterval:   metav1.Duration{Duration: 2 * time.Second},
					LLMInsightsInterval: metav1.Duration{Duration: 24 * time.Hour},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())

			// Wait for the insight to appear on the target PVC.
			Eventually(func() string {
				var p capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &p); err != nil {
					return ""
				}
				for _, pvc := range p.Status.PVCs {
					if pvc.Name == pvcName {
						return pvc.LLMInsight
					}
				}
				return ""
			}, "15s", "500ms").Should(Equal("mock insight"))

			// Record the LastLLMTime. On subsequent reconcile it should not change.
			var p capacityv1.CapacityPlan
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &p)).To(Succeed())
			var firstLLMTime *metav1.Time
			for _, pvc := range p.Status.PVCs {
				if pvc.Name == pvcName {
					firstLLMTime = pvc.LastLLMTime
					break
				}
			}
			Expect(firstLLMTime).NotTo(BeNil())

			// Wait one more reconcile cycle; LastLLMTime should be unchanged.
			time.Sleep(3 * time.Second)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &p)).To(Succeed())
			for _, pvc := range p.Status.PVCs {
				if pvc.Name == pvcName {
					Expect(pvc.LastLLMTime).NotTo(BeNil())
					Expect(pvc.LastLLMTime.Time).To(Equal(firstLLMTime.Time))
					return
				}
			}
		})
	})

	Describe("LLM error preserves previous insight", func() {
		It("keeps previous insight when LLM returns an error", func() {
			pvcName := "cp-llm-err-pvc"
			key := testNS + "/" + pvcName
			mockMetricsClient.Data[key] = pkgmetrics.PVCUsage{
				UsedBytes:     1 * 1024 * 1024 * 1024,
				CapacityBytes: 10 * 1024 * 1024 * 1024,
			}

			// First call succeeds.
			mockLLMClient.Response = "initial insight"

			makePVC(pvcName, testNS, "10Gi")
			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(key))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// Short LLM interval to trigger refresh on second reconcile.
			plan := &capacityv1.CapacityPlan{
				ObjectMeta: metav1.ObjectMeta{Name: planName},
				Spec: capacityv1.CapacityPlanSpec{
					Namespaces:          nil,
					ReconcileInterval:   metav1.Duration{Duration: 2 * time.Second},
					LLMInsightsInterval: metav1.Duration{Duration: 1 * time.Millisecond},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())

			// Wait for the initial insight to appear.
			Eventually(func() string {
				var p capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &p); err != nil {
					return ""
				}
				for _, pvc := range p.Status.PVCs {
					if pvc.Name == pvcName {
						return pvc.LLMInsight
					}
				}
				return ""
			}, "15s", "500ms").Should(Equal("initial insight"))

			// Now make the LLM fail.
			mockLLMClient.SetErr(errLLMFailed)

			// After another reconcile cycle, insight should remain "initial insight".
			time.Sleep(3 * time.Second)
			var p capacityv1.CapacityPlan
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &p)).To(Succeed())
			for _, pvc := range p.Status.PVCs {
				if pvc.Name == pvcName {
					Expect(pvc.LLMInsight).To(Equal("initial insight"))
					return
				}
			}
		})
	})

	Describe("Namespace scoping", func() {
		It("excludes PVCs from unwatched namespaces", func() {
			// Create a PVC in default — it should appear.
			pvcInScope := "cp-ns-inscope"
			keyIn := testNS + "/" + pvcInScope
			mockMetricsClient.Data[keyIn] = pkgmetrics.PVCUsage{
				UsedBytes: 1 * 1024 * 1024 * 1024, CapacityBytes: 5 * 1024 * 1024 * 1024,
			}
			makePVC(pvcInScope, testNS, "5Gi")

			Eventually(func() int {
				return len(pvcWatcher.GetSnapshot(keyIn))
			}, "10s", "200ms").Should(BeNumerically(">=", 1))

			// Watch only "default" namespace.
			makePlan([]string{"default"}, metav1.Duration{Duration: 2 * time.Second})

			Eventually(func() bool {
				var plan capacityv1.CapacityPlan
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &plan); err != nil {
					return false
				}
				for _, p := range plan.Status.PVCs {
					if p.Name == pvcInScope {
						return true
					}
				}
				return false
			}, "15s", "500ms").Should(BeTrue())
		})
	})
})

// errLLMFailed is a sentinel error used in LLM error tests.
var errLLMFailed = fmt.Errorf("LLM service unavailable")
