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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/netip"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/internal/controller"
	"github.com/mohanraj00/koodam/internal/gateway"
	runmetrics "github.com/mohanraj00/koodam/internal/metrics"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(corev1beta1.AddToScheme(scheme))
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
//
// With namespaces, the cache has the namespaced objects of these namespaces only. Then the
// controllers of this manager do not see an object of a different install of the cluster. A
// cluster-scoped object, for example a Profile, is in the cache of each install.
func workspaceCache(namespaces []string) cache.Options {
	req, err := labels.NewRequirement(controller.WorkspaceLabel, selection.Exists, nil)
	utilruntime.Must(err)
	sel := labels.NewSelector().Add(*req)
	opts := cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.ConfigMap{}:   {Label: sel},
		&appsv1.StatefulSet{}: {Label: sel},
	}}
	if len(namespaces) > 0 {
		opts.DefaultNamespaces = map[string]cache.Config{}
		for _, ns := range namespaces {
			opts.DefaultNamespaces[ns] = cache.Config{}
		}
	}
	return opts
}

// installScope examines the zone of the install and gives back the namespaces that the manager
// reads. A zone is a DNS label. An empty list is each namespace of the cluster.
func installScope(zone, namespaces string) ([]string, error) {
	if zone != "" {
		if errs := validation.IsDNS1123Label(zone); len(errs) > 0 {
			return nil, fmt.Errorf("the zone %q is not valid: %s", zone, strings.Join(errs, "; "))
		}
	}
	return splitNamespaces(namespaces)
}

// nolint:gocyclo
// loadClaim reads the JWKS file of the App claim check and examines the configuration. With no
// issuer, the check is off and the other values have no effect.
func loadClaim(c controller.AppClaimCheck, jwksFile string) (controller.AppClaimCheck, error) {
	if c.Issuer == "" {
		return controller.AppClaimCheck{}, nil
	}
	cfg := appclaim.Config{Issuer: c.Issuer, Audience: c.Audience, CallerClaim: c.CallerClaim, TenantClaim: c.TenantClaim,
		Zone: c.Zone, ZoneClaim: c.ZoneClaim, RequireZoneClaim: c.RequireZoneClaim}
	if err := cfg.Valid(); err != nil {
		return c, err
	}
	if c.Image == "" {
		return c, errors.New("--installer-image is necessary for the App claim check")
	}
	data, err := os.ReadFile(jwksFile) //nolint:gosec // The operator configures the path.
	if err != nil {
		return c, errors.New("--app-claim-jwks-file is not readable")
	}
	if _, err := appclaim.ParseKeySet(data); err != nil {
		return c, err
	}
	c.JWKS = string(data)
	return c, nil
}

// loadTenantDefaults examines the flags for a tenant label with no TenantQuota. rate is a
// quantity of requests for each second, or empty. The flags need a tenant claim, because the
// claim check has no tenant label without it.
func loadTenantDefaults(c controller.AppClaimCheck, rate string, burst int, refuse bool) (
	controller.AppClaimCheck, error) {
	if rate == "" && burst == 0 && !refuse {
		return c, nil
	}
	if c.Issuer == "" || c.TenantClaim == "" {
		return c, errors.New("the tenant flags need --app-claim-issuer and --app-claim-tenant-claim")
	}
	if burst < 0 || burst > maxTenantRate || (burst > 0 && rate == "") {
		return c, errors.New("--tenant-default-burst must be from 0 to 1000000, and it needs --tenant-default-rate")
	}
	c.Tenants = controller.AppTenantLimits{DefaultBurst: int32(burst), RefuseUnknown: refuse}
	if rate != "" {
		q, err := resource.ParseQuantity(rate)
		if err != nil || q.Sign() <= 0 || q.AsApproximateFloat64() > maxTenantRate {
			return c, errors.New("--tenant-default-rate must be a quantity more than 0, and 1000000 or less")
		}
		c.Tenants.DefaultRate = q.AsApproximateFloat64()
	}
	return c, nil
}

// maxTenantRate is the largest default rate and the largest default burst of a tenant.
const maxTenantRate = 1_000_000

// setupApps adds the App controllers to the manager. It refuses a read interval of the tenant
// use below 1 second.
func setupApps(mgr ctrl.Manager, versions *controller.AppVersionReconciler,
	quotas *controller.TenantQuotaReconciler) error {
	if quotas.Interval < time.Second {
		return errors.New("the flag --tenant-usage-interval must be 1s or more")
	}
	if err := versions.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("appversion: %w", err)
	}
	if err := quotas.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("tenantquota: %w", err)
	}
	limits := &controller.TenantLimitsReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(),
		Claim: versions.Claim}
	if err := limits.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("tenantlimits: %w", err)
	}
	apps := &controller.AppReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader()}
	if err := apps.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	return nil
}

// checkClaimPolicy refuses the App claim check if the controller cannot make the NetworkPolicy
// of a version. With no policy, a pod of the cluster can call the app container with no claim.
// setupHoldAndProfile sets up the Hold and the Profile controllers.
func setupHoldAndProfile(mgr ctrl.Manager, holds *controller.HoldReconciler) error {
	if err := holds.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("hold: %w", err)
	}
	if err := (&controller.ProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	return nil
}

func checkClaimPolicy(c controller.AppClaimCheck, e controller.EgressProxy) error {
	if c.Issuer != "" && (e.IP == "" || e.Namespace == "" || len(e.PodLabels) == 0) {
		return errors.New("the App claim check needs --egress-proxy-ip, --egress-proxy-namespace and " +
			"--egress-proxy-pod-labels, because only the NetworkPolicy of a version blocks a call with no claim")
	}
	return nil
}

func loadAppAPIServerEgressOrExit(raw string, e controller.EgressProxy) []networkingv1.NetworkPolicyEgressRule {
	rules, err := loadAppAPIServerEgress(raw, e)
	if err != nil {
		setupLog.Error(err, "The flag --app-api-server-egress is not valid")
		os.Exit(1)
	}
	return rules
}

func loadAppAPIServerEgress(raw string, e controller.EgressProxy) ([]networkingv1.NetworkPolicyEgressRule, error) {
	if raw == "" {
		return nil, nil
	}
	if !json.Valid([]byte(raw)) || !strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return nil, errors.New("the value must be a JSON list of NetworkPolicyEgressRule")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input []*networkingv1.NetworkPolicyEgressRule
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	if len(input) > 0 && (e.IP == "" || e.Namespace == "" || len(e.PodLabels) == 0) {
		return nil, errors.New("the rules need --egress-proxy-ip, --egress-proxy-namespace and --egress-proxy-pod-labels")
	}
	rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(input))
	for _, rule := range input {
		if rule == nil {
			return nil, errors.New("the list must not contain null")
		}
		if err := checkAPIServerRule(*rule); err != nil {
			return nil, err
		}
		// Use the default of the API server so the controller can compare the policies.
		for i := range rule.Ports {
			if rule.Ports[i].Protocol == nil {
				rule.Ports[i].Protocol = new(corev1.ProtocolTCP)
			}
		}
		rules = append(rules, *rule)
	}
	return rules, nil
}

// checkAPIServerRule accepts only a rule to API server endpoint addresses. Each rule needs one or
// more peers and one or more ports. Each peer is an ipBlock with one address (/32 or /128) and no
// except list, so a rule cannot open a wide range or a pod selector.
func checkAPIServerRule(rule networkingv1.NetworkPolicyEgressRule) error {
	if len(rule.To) == 0 || len(rule.Ports) == 0 {
		return errors.New("each rule needs a peer in to and a port")
	}
	for _, peer := range rule.To {
		if peer.IPBlock == nil || peer.PodSelector != nil || peer.NamespaceSelector != nil {
			return errors.New("each peer must be an ipBlock with the address of an API server endpoint")
		}
		prefix, err := netip.ParsePrefix(peer.IPBlock.CIDR)
		if err != nil || prefix.Bits() != prefix.Addr().BitLen() || len(peer.IPBlock.Except) > 0 {
			return fmt.Errorf("the ipBlock %q must be one address (/32 or /128) with no except list", peer.IPBlock.CIDR)
		}
		if err := checkAPIServerAddr(peer.IPBlock.CIDR, prefix.Addr()); err != nil {
			return err
		}
	}
	for _, port := range rule.Ports {
		if port.Port == nil || port.EndPort != nil || port.Port.Type != intstr.Int ||
			port.Port.IntVal < 1 || port.Port.IntVal > 65535 {
			return errors.New("each port of a rule must be one port number from 1 to 65535")
		}
		if port.Protocol != nil && *port.Protocol != corev1.ProtocolTCP {
			return errors.New("each port of a rule must use TCP")
		}
	}
	return nil
}

// metadataAddrs are cloud metadata addresses that are not link-local: the IPv6 address of the AWS
// metadata service, and the metadata address of Alibaba Cloud.
var metadataAddrs = []netip.Addr{netip.MustParseAddr("fd00:ec2::254"), netip.MustParseAddr("100.100.100.200")}

// checkAPIServerAddr refuses an address that is not the address of an API server endpoint: a
// loopback, link-local, unspecified or multicast address, or a cloud metadata address. An IPv4
// address in the IPv6 form gets the same check. A rule to such an address gives the App pods a
// direct path to the node or to the metadata service, with no span (#501).
func checkAPIServerAddr(cidr string, addr netip.Addr) error {
	addr = addr.Unmap()
	var class string
	switch {
	case addr.IsLoopback():
		class = "loopback"
	case addr.IsLinkLocalUnicast():
		class = "link-local"
	case addr.IsUnspecified():
		class = "unspecified"
	case addr.IsMulticast():
		class = "multicast"
	case slices.Contains(metadataAddrs, addr):
		class = "cloud metadata"
	default:
		return nil
	}
	return fmt.Errorf("the ipBlock %q is a %s address, not the address of an API server endpoint", cidr, class)
}

// checkEgress refuses an egress proxy IP that is not an IPv4 address, and a port or a cluster
// domain that is not valid. The pods use the IP as their DNS server.
func checkEgress(e controller.EgressProxy) error {
	if e.IP == "" {
		return nil
	}
	if a, err := netip.ParseAddr(e.IP); err != nil || !a.Is4() {
		return fmt.Errorf("--egress-proxy-ip=%q is not an IPv4 address", e.IP)
	}
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("--egress-proxy-port=%d is out of range", e.Port)
	}
	if errs := validation.IsDNS1123Subdomain(e.ClusterDomain); len(errs) > 0 {
		return fmt.Errorf("--cluster-domain=%q is not valid: %s", e.ClusterDomain, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Label(e.Namespace); e.Namespace != "" && len(errs) > 0 {
		return fmt.Errorf("--egress-proxy-namespace=%q is not valid: %s", e.Namespace, strings.Join(errs, "; "))
	}
	return nil
}

// digestImage matches an image reference with a sha256 digest. The digest pins the Nookku files.
var digestImage = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// splitNamespaces splits a comma-separated list of namespaces. It ignores an empty item, and it
// refuses a name that is not a DNS-1123 label.
func splitNamespaces(list string) ([]string, error) {
	var namespaces []string
	for ns := range strings.SplitSeq(list, ",") {
		if ns == "" {
			continue
		}
		if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
			return nil, fmt.Errorf("the namespace %q is not valid: %s", ns, strings.Join(errs, "; "))
		}
		namespaces = append(namespaces, ns)
	}
	return namespaces, nil
}

func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var webhookPort int
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var gatewayURL, gatewayKeyFile, agentGatewayURL, installerImage, nookkuImage, workspaceImage string
	var workspaceStorageClass string
	var testKeySingleGateway bool
	var testKeyRevokeWait time.Duration
	var keyGrace, tenantUsageInterval time.Duration
	var egress controller.EgressProxy
	var egressPodLabels, appIngressNamespaces, appAPIServerEgress, claimJWKSFile string
	var claim controller.AppClaimCheck
	var jobTTLSeconds, tenantDefaultBurst int
	var tenantDefaultRate, zone, watchNamespaces string
	var tenantRefuseUnknown bool
	var agentIngressEnabled, appsEnabled bool
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
	flag.StringVar(&egress.IP, "egress-proxy-ip", "",
		"The ClusterIP of the egress proxy. If set, the pods use it as their DNS server and their gateway.")
	flag.IntVar(&egress.Port, "egress-proxy-port", 3128, "The HTTP port of the egress proxy.")
	flag.StringVar(&egress.Namespace, "egress-proxy-namespace", "",
		"The namespace of the egress proxy pods. With --egress-proxy-pod-labels, each AppVersion gets a "+
			"NetworkPolicy that permits egress to these pods.")
	flag.StringVar(&egressPodLabels, "egress-proxy-pod-labels", "",
		"The labels of the egress proxy pods, as key=value pairs with a comma between them.")
	flag.StringVar(&appIngressNamespaces, "app-ingress-namespaces", "",
		"The namespaces whose pods can send traffic to the App pods, with a comma between them. Use the "+
			"namespace of the Gateway data plane. If it is empty, the NetworkPolicy of an AppVersion permits no ingress.")
	flag.StringVar(&appAPIServerEgress, "app-api-server-egress", "",
		"A JSON list of NetworkPolicyEgressRule for the API server. It applies to AppVersions with template.rules.")
	flag.StringVar(&claim.Issuer, "app-claim-issuer", "",
		"The issuer of the signed claim that each request to an App must have. If it is empty, the Apps have no claim check.")
	flag.StringVar(&claim.Audience, "app-claim-audience", "",
		"A value that the claim aud of a request to an App must have.")
	flag.StringVar(&claim.CallerClaim, "app-claim-caller-claim", "sub", "The claim with the caller label.")
	flag.StringVar(&claim.TenantClaim, "app-claim-tenant-claim", "",
		"The claim with the tenant label. If it is empty, the check reads no tenant label.")
	flag.BoolVar(&claim.TenantToken, "app-claim-tenant-token", true,
		"With a tenant claim, the check container signs a tenant token for the tenant of each request. "+
			"The egress proxy then needs --tenant-tokens.")
	flag.StringVar(&claim.ZoneClaim, "app-claim-zone-claim", "",
		"The claim with the zone of the caller. With it, the check refuses a caller of a zone that is not --zone.")
	flag.BoolVar(&claim.RequireZoneClaim, "app-claim-require-zone", false,
		"Refuse a token with no zone claim. It needs --app-claim-zone-claim.")
	flag.StringVar(&claimJWKSFile, "app-claim-jwks-file", "",
		"The file with the JWKS of the issuer. The manager reads it at its start.")
	flag.StringVar(&egress.ClusterDomain, "cluster-domain", "cluster.local", "The DNS domain of the cluster.")
	flag.StringVar(&zone, "zone", "", "The zone of this install, as a DNS label. Empty: the install has no zone.")
	flag.StringVar(&watchNamespaces, "watch-namespaces", "",
		"The namespaces of this install, separated by commas. The manager reads the namespaced objects of these "+
			"namespaces only. Empty: each namespace. It is necessary if the cluster has 2 installs.")
	flag.DurationVar(&keyGrace, "key-grace", time.Minute, "The time that is added to deadlineSeconds for the key TTL.")
	flag.IntVar(&jobTTLSeconds, "job-ttl-seconds", controller.DefaultJobTTLSeconds,
		"The time in seconds that the controller keeps a run Job and its pods after the Job ends. "+
			"The value must be from 60 to 2147483647.")
	flag.StringVar(&installerImage, "installer-image", "",
		"The image with /harness-install, for an Agent or a Workspace with a harness. The chart gives the manager image.")
	flag.StringVar(&nookkuImage, controller.NookkuImageFlag, "",
		"The Nookku data image with the pinned Nookku files, for a harness with the nookku plugin (#83). "+
			"The reference must have a digest (@sha256:).")
	flag.StringVar(&workspaceImage, "workspace-image", defaultWorkspaceImage(),
		"The workspace image for a Workspace with no spec.image.")
	flag.StringVar(&workspaceStorageClass, "workspace-storage-class", "",
		"The storage class of the home claim of a new Workspace. Empty: the default of the cluster.")
	flag.BoolVar(&testKeySingleGateway, "test-key-single-instance-gateway", false,
		"Issue TestKeys. Set it only if the gateway has one replica and one worker (#192).")
	flag.DurationVar(&testKeyRevokeWait, "test-key-revoke-wait", controller.DefaultTestKeyRevokeWait,
		"The time after a TestKey block before the spend read. Use the gateway auth cache TTL plus a margin.")
	flag.BoolVar(&appsEnabled, "apps-enabled", false,
		"If set, the manager runs the App and AppVersion controllers. The App CRDs and the App role in config/apps "+
			"must be installed first.")
	flag.StringVar(&tenantDefaultRate, "tenant-default-rate", "",
		"The rate limit of a tenant label with no TenantQuota, in requests for each second. Empty: no limit.")
	flag.IntVar(&tenantDefaultBurst, "tenant-default-burst", 0,
		"The burst of a tenant label with no TenantQuota. 0: the rate, with a minimum of 1.")
	flag.BoolVar(&tenantRefuseUnknown, "tenant-refuse-unknown", false,
		"If set, the claim check refuses a tenant label with no TenantQuota.")
	flag.DurationVar(&tenantUsageInterval, "tenant-usage-interval", controller.DefaultTenantUsageInterval,
		"The time between 2 reads of the use of a tenant from the gateway. It is 1s or more.")
	flag.BoolVar(&agentIngressEnabled, "agent-ingress-enabled", false,
		"If set, the controller provisions a new Agent that sets spec.ingressPort. Do not set it before the "+
			"protected install of the Agent ingress is complete. An Agent with a stored status.keyHash can "+
			"recover with each value, and the revoke does not use it.")
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

	watched, err := installScope(zone, watchNamespaces)
	if err != nil {
		setupLog.Error(err, "The flag --zone or --watch-namespaces is not valid")
		os.Exit(1)
	}
	setupLog.Info("The scope of this install", "zone", zone, "namespaces", watched)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Cache:                  workspaceCache(watched),
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
	if err := metrics.Registry.Register(runmetrics.NewCollector(mgr.GetCache())); err != nil {
		setupLog.Error(err, "Failed to register Agent metrics")
		os.Exit(1)
	}

	if gatewayURL == "" || gatewayKeyFile == "" {
		setupLog.Error(nil, "The flags --gateway-url and --gateway-master-key-file are required")
		os.Exit(1)
	}
	if nookkuImage != "" && !digestImage.MatchString(nookkuImage) {
		setupLog.Error(nil, "The flag --nookku-image must end with a digest @sha256:<64 hex characters>")
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
	if egress.PodLabels, err = labels.ConvertSelectorToLabelsMap(egressPodLabels); err != nil {
		setupLog.Error(err, "--egress-proxy-pod-labels is not valid")
		os.Exit(1)
	}
	ingressNamespaces, err := splitNamespaces(appIngressNamespaces)
	if err != nil {
		setupLog.Error(err, "--app-ingress-namespaces has a namespace that is not valid")
		os.Exit(1)
	}
	claim.Image = installerImage
	claim.Zone = zone
	if claim, err = loadClaim(claim, claimJWKSFile); err == nil {
		claim, err = loadTenantDefaults(claim, tenantDefaultRate, tenantDefaultBurst, tenantRefuseUnknown)
	}
	if err = errors.Join(err, checkClaimPolicy(claim, egress)); err != nil {
		setupLog.Error(err, "The App claim check flags or the tenant flags are not valid")
		os.Exit(1)
	}
	if err := checkEgress(egress); err != nil {
		setupLog.Error(err, "The egress proxy flags are not valid")
		os.Exit(1)
	}
	agentGatewayURL = egress.GatewayURL(agentGatewayURL)
	gw := gateway.NewLiteLLM(gatewayURL, strings.TrimSpace(string(masterKey)))
	// Each reconciler gets the gateway with a count of its failed key blocks (#130).
	revokeFailures := registerRevokeFailures()
	// The events of a stop by a Hold (#185).
	recorder := mgr.GetEventRecorder("koodam")
	if err := (&controller.AgentReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		APIReader:      mgr.GetAPIReader(),
		Gateway:        revokeFailures.Gateway(gw, "Agent"),
		GatewayURL:     strings.TrimRight(agentGatewayURL, "/"),
		Egress:         egress,
		Grace:          keyGrace,
		InstallerImage: installerImage,
		PresetImages:   map[string]string{controller.NookkuImageFlag: nookkuImage},
		JobTTL:         int32(jobTTLSeconds),
		IngressEnabled: agentIngressEnabled,
		Recorder:       recorder,
		Namespaces:     watched,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "agent")
		os.Exit(1)
	}
	if err := setupHoldAndProfile(mgr, &controller.HoldReconciler{
		Client:      mgr.GetClient(),
		Recorder:    recorder,
		AppsEnabled: appsEnabled,
		APIReader:   mgr.GetAPIReader(),
		Namespaces:  watched,
		EgressProxy: egress.IP != "",
	}); err != nil {
		setupLog.Error(err, "Failed to create the Hold or the Profile controller")
		os.Exit(1)
	}
	if err := (&controller.WorkspaceReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		APIReader:      mgr.GetAPIReader(),
		Image:          workspaceImage,
		StorageClass:   workspaceStorageClass,
		InstallerImage: installerImage,
		PresetImages:   map[string]string{controller.NookkuImageFlag: nookkuImage},
		Gateway:        revokeFailures.Gateway(gw, "Workspace"),
		GatewayURL:     strings.TrimRight(agentGatewayURL, "/"),
		Egress:         egress,
		Recorder:       recorder,
		Namespaces:     watched,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "workspace")
		os.Exit(1)
	}
	if err := (&controller.TestKeyReconciler{
		Client:                mgr.GetClient(),
		APIReader:             mgr.GetAPIReader(),
		Gateway:               revokeFailures.Gateway(gw, "TestKey"),
		GatewayURL:            strings.TrimRight(agentGatewayURL, "/"),
		SingleInstanceGateway: testKeySingleGateway,
		RevokeWait:            testKeyRevokeWait,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "testkey")
		os.Exit(1)
	}
	if appsEnabled {
		apiServerEgress := loadAppAPIServerEgressOrExit(appAPIServerEgress, egress)
		err := setupApps(mgr, &controller.AppVersionReconciler{
			Client:            mgr.GetClient(),
			Scheme:            mgr.GetScheme(),
			APIReader:         mgr.GetAPIReader(),
			Gateway:           revokeFailures.Gateway(gw, "AppVersion"),
			GatewayURL:        strings.TrimRight(agentGatewayURL, "/"),
			Egress:            egress,
			APIServerEgress:   apiServerEgress,
			IngressNamespaces: ingressNamespaces,
			Claim:             claim,
			Recorder:          recorder,
			Namespaces:        watched,
		}, &controller.TenantQuotaReconciler{Client: mgr.GetClient(), Customers: gw, Interval: tenantUsageInterval})
		if err != nil {
			setupLog.Error(err, "Failed to create the App controllers")
			os.Exit(1)
		}
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

// registerRevokeFailures registers the count of the failed key blocks of the revoke paths.
func registerRevokeFailures() *runmetrics.RevokeFailures {
	r := runmetrics.NewRevokeFailures()
	if err := metrics.Registry.Register(r); err != nil {
		setupLog.Error(err, "Failed to register the revoke metrics")
		os.Exit(1)
	}
	return r
}
