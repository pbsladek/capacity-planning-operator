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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/controller"
	"github.com/pbsladek/capacity-planning-operator/internal/metrics"
	_ "github.com/pbsladek/capacity-planning-operator/internal/operator" // registers Prometheus metrics in init()
)

const defaultSampleRetention = 720
const defaultBackfillStep = 5 * time.Minute

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(capacityv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var prometheusURL string
	var debug bool
	var tlsOpts []func(*tls.Config)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&prometheusURL, "prometheus-url", "",
		"Base URL of the Prometheus instance to query for PVC metrics (e.g. http://prometheus:9090). "+
			"If empty, PVC usage is not collected from Prometheus.")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging (equivalent to --zap-log-level=debug).")

	opts := crzap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	if debug {
		lvl := zap.NewAtomicLevelAt(zapcore.DebugLevel)
		opts.Level = &lvl
	}

	ctrl.SetLogger(crzap.New(crzap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), we set the tlsOpts to disable HTTP/2 to mitigate
	// CVE-2023-44487 (HTTP/2 Rapid Reset Attack).
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "capacityplanning.pbsladek.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Build the metrics client. Fall back to a no-op mock when no Prometheus URL is configured.
	var metricsClient metrics.PVCMetricsClient
	if prometheusURL != "" {
		metricsClient = metrics.NewPrometheusClient(prometheusURL)
		setupLog.Info("Prometheus metrics client configured", "url", prometheusURL)
	} else {
		setupLog.Info("no --prometheus-url configured; PVC usage metrics will not be collected")
		metricsClient = &metrics.MockPVCMetricsClient{}
	}

	// Wire PVCWatcherReconciler (default ring-buffer retention: 720 samples).
	pvcWatcher := controller.NewPVCWatcherReconciler(mgr.GetClient(), metricsClient, defaultSampleRetention)
	if err = pvcWatcher.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PVCWatcher")
		os.Exit(1)
	}
	backfillConfigured := prometheusURL != ""
	backfillSuccessfulPVCs := 0
	backfillErrMsg := ""
	if prometheusURL != "" {
		start, end := backfillWindow(defaultSampleRetention, defaultBackfillStep, time.Now())
		backfillCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		n, backfillErr := pvcWatcher.BackfillAllPVCs(backfillCtx, start, end, defaultBackfillStep)
		backfillSuccessfulPVCs = n
		if backfillErr != nil {
			backfillErrMsg = backfillErr.Error()
			setupLog.Error(backfillErr, "PVC startup backfill completed with errors", "successfulPVCs", n)
		} else {
			setupLog.Info("PVC startup backfill completed", "successfulPVCs", n)
		}
	}

	operatorNamespace := os.Getenv("POD_NAMESPACE")
	if operatorNamespace == "" {
		operatorNamespace = "default"
	}

	// Wire CapacityPlanReconciler. LLM provider is selected from CapacityPlan spec.
	reconciler := &controller.CapacityPlanReconciler{
		Client:                        mgr.GetClient(),
		Scheme:                        mgr.GetScheme(),
		Watcher:                       pvcWatcher,
		DefaultMetricsClient:          metricsClient,
		DefaultRetention:              defaultSampleRetention,
		OperatorNamespace:             operatorNamespace,
		StartupBackfillConfigured:     backfillConfigured,
		StartupBackfillSuccessfulPVCs: backfillSuccessfulPVCs,
		StartupBackfillError:          backfillErrMsg,
	}
	if err = reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CapacityPlan")
		os.Exit(1)
	}
	if err = (&controller.CapacityPlanNotificationReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CapacityPlanNotification")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func backfillWindow(retention int, step time.Duration, now time.Time) (start, end time.Time) {
	if retention < 1 {
		retention = defaultSampleRetention
	}
	if step <= 0 {
		step = defaultBackfillStep
	}
	end = now
	start = end.Add(-time.Duration(retention) * step)
	return start, end
}
