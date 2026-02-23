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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/llm"
	pkgmetrics "github.com/pbsladek/capacity-planning-operator/internal/metrics"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc

// Package-level test doubles exposed to all controller test files.
var mockMetricsClient *pkgmetrics.MockPVCMetricsClient
var mockLLMClient *llm.MockInsightGenerator
var pvcWatcher *PVCWatcherReconciler

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,

		// The BinaryAssetsDirectory is only required if you want to run the tests directly
		// without call the makefile target test. If not informed it will look for the
		// default path defined in controller-runtime which is /usr/local/kubebuilder/.
		// Note that you must have the required binaries setup under the bin/k8s/
		// directory to make it work. That's why we invoke the setup-envtest binary.
		BinaryAssetsDirectory: getFirstFoundEnvTestBinaryDir(),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = capacityv1.AddToScheme(testEnv.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: testEnv.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("setting up controller manager")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: testEnv.Scheme,
		// Disable the metrics server in tests to avoid port conflicts.
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	// Create shared mock doubles for all tests.
	mockMetricsClient = &pkgmetrics.MockPVCMetricsClient{}
	mockLLMClient = &llm.MockInsightGenerator{}

	// Wire PVCWatcher with mock metrics client (small capacity for tests).
	pvcWatcher = NewPVCWatcherReconciler(mgr.GetClient(), mockMetricsClient, 10)
	Expect(pvcWatcher.SetupWithManager(mgr)).To(Succeed())

	// Wire CapacityPlanReconciler with mock LLM client.
	capacityReconciler := &CapacityPlanReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Watcher:   pvcWatcher,
		LLMClient: mockLLMClient,
	}
	Expect(capacityReconciler.SetupWithManager(mgr)).To(Succeed())

	By("starting controller manager")
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST binaries are installed at ./bin/k8s/ after running:
//
//	make envtest && ./bin/setup-envtest use <k8s-version> --bin-dir ./bin/k8s
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read envtest binary directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	// Fallback for systems where binaries are installed globally.
	return fmt.Sprintf("/usr/local/kubebuilder/bin/%s/%s",
		runtime.GOOS, runtime.GOARCH)
}
