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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tutorialv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

var _ = Describe("Sample controller", func() {
	Context("Sample controller test", func() {
		const sampleName = "test-sample"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      sampleName,
			Namespace: "default",
		}

		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(time.Second)

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
			}
			// Namespace may already exist; ignore AlreadyExists errors.
			err := k8sClient.Create(ctx, namespace)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			By("Creating the custom resource for the Kind Sample")
			sample := &tutorialv1.Sample{}
			err = k8sClient.Get(ctx, typeNamespacedName, sample)
			if err != nil && errors.IsNotFound(err) {
				resource := &tutorialv1.Sample{
					ObjectMeta: metav1.ObjectMeta{
						Name:      sampleName,
						Namespace: "default",
					},
					Spec: tutorialv1.SampleSpec{
						Foo:  "bar",
						Size: 1,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("Deleting the custom resource for the Kind Sample")
			sample := &tutorialv1.Sample{}
			err := k8sClient.Get(ctx, typeNamespacedName, sample)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Delete(ctx, sample)).To(Succeed())
			}).Should(Succeed())
		})

		It("should successfully reconcile a custom resource for Sample", func() {
			By("Checking if the custom resource was successfully created")
			Eventually(func(g Gomega) {
				found := &tutorialv1.Sample{}
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, found)).To(Succeed())
			}).Should(Succeed())

			By("Reconciling the custom resource created")
			sampleReconciler := &SampleReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := sampleReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the resource still exists after reconciliation")
			found := &tutorialv1.Sample{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, found)).To(Succeed())
			Expect(found.Spec.Foo).To(Equal("bar"))
		})
	})
})
