/*
Copyright 2026 Mohan Raj.

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
	"crypto/tls"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/controller"
	"github.com/mohanraj00/koodam/internal/gateway"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// releaseVersion is the format of a release version of the Go module, for example v0.2.0 or
// v0.2.0-rc.1.
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// pseudoVersion is the end of a Go pseudo-version, for example -20261021000000-abcdef123456. No
// image has this tag.
var pseudoVersion = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}$`)

// defaultWorkspaceImage is the workspace image of the same release as the controller.
func defaultWorkspaceImage() string {
	v := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		v = bi.Main.Version
	}
	return workspaceImageFor(v)
}

// workspaceImageFor returns the workspace image for the module version v. If v is not a release
// version, the tag is "latest".
func workspaceImageFor(v string) string {
	tag := "latest"
	if releaseVersion.MatchString(v) && !pseudoVersion.MatchString(v) {
		tag = strings.TrimPrefix(v, "v")
	}
	return "ghcr.io/mohanraj00/koodam-workspace:" + tag
}

// workspaceCache keeps only the ConfigMaps and the StatefulSets with the Workspace label in the
// cache. Thus the controller does not keep all these objects of the cluster in memory. This limit
// applies to all the controllers of the manager. A controller that reads other ConfigMaps or
// StatefulSets (for example koodam-globals) must use the API reader or change this cache.
func workspaceCache() cache.Options {
	req, err := labels.NewRequirement(controller.WorkspaceLabel, selection.Exists, nil)
	utilruntime.Must(err)
	sel := labels.NewSelector().Add(*req)
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.ConfigMap{}:   {Label: sel},
		&appsv1.StatefulSet{}: {Label: sel},
	}}
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var webhookPort int
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var gatewayURL, gatewayKeyFile, agentGatewayURL, installerImage, workspaceImage string
	var keyGrace time.Duration
	var jobTTLSeconds int
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	flag.StringVar(&gatewayURL, "gateway-url", "", "The LiteLLM admin address that the controller uses.")
	flag.StringVar(&gatewayKeyFile, "gateway-master-key-file", "",
		"The file with the LiteLLM master key. The key is never a flag value.")
	flag.StringVar(&agentGatewayURL, "agent-gateway-url", "",
		"The gateway address for the agent pods. The default is the value of --gateway-url.")
	flag.DurationVar(&keyGrace, "key-grace", time.Minute, "The time that is added to deadlineSeconds for the key TTL.")
	flag.IntVar(&jobTTLSeconds, "job-ttl-seconds", controller.DefaultJobTTLSeconds,
		"The time in seconds that the controller keeps a run Job and its pods after the Job ends. "+
			"The value must be from 60 to 2147483647.")
	flag.StringVar(&installerImage, "installer-image", "",
		"The image with /harness-install, for an Agent with a harness. The chart gives the manager image.")
	flag.StringVar(&workspaceImage, "workspace-image", defaultWorkspaceImage(),
		"The workspace image for a Workspace with no spec.image.")
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
		Port:    webhookPort,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Cache:                  workspaceCache(),
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "1f8d0d9c.koodam.dev",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if gatewayURL == "" || gatewayKeyFile == "" {
		setupLog.Error(nil, "The flags --gateway-url and --gateway-master-key-file are required")
		os.Exit(1)
	}
	masterKey, err := os.ReadFile(gatewayKeyFile)
	if err != nil {
		setupLog.Error(err, "Failed to read the gateway master key file")
		os.Exit(1)
	}
	if jobTTLSeconds < controller.MinJobTTLSeconds || jobTTLSeconds > math.MaxInt32 {
		setupLog.Error(fmt.Errorf("--job-ttl-seconds=%d is out of range", jobTTLSeconds),
			"The flag --job-ttl-seconds must be from 60 to 2147483647")
		os.Exit(1)
	}
	if agentGatewayURL == "" {
		agentGatewayURL = gatewayURL
	}
	gw := gateway.NewLiteLLM(gatewayURL, strings.TrimSpace(string(masterKey)))
	if err := (&controller.AgentReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		APIReader:      mgr.GetAPIReader(),
		Gateway:        gw,
		GatewayURL:     strings.TrimRight(agentGatewayURL, "/"),
		Grace:          keyGrace,
		InstallerImage: installerImage,
		JobTTL:         int32(jobTTLSeconds),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "agent")
		os.Exit(1)
	}
	if err := (&controller.ProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "profile")
		os.Exit(1)
	}
	if err := (&controller.WorkspaceReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		APIReader:  mgr.GetAPIReader(),
		Image:      workspaceImage,
		Gateway:    gw,
		GatewayURL: strings.TrimRight(agentGatewayURL, "/"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "workspace")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
