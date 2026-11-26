//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/internal/egress"
	"github.com/mohanraj00/koodam/test/utils"
)

// otherTenant is the label of a tenant that sends no request to the app.
const otherTenant = "other-tenant"

// appServer is the app of the test. It answers each GET with the name of its AppVersion. For the
// path /model, it makes a model call and answers with the status of that call. The body and a
// header of the model call have the label of a different tenant, in each field from which the
// gateway can read a customer. With the query "token", the call has the tenant token of the
// request.
const appServer = `
import http.server, json, os, urllib.error, urllib.request
OTHER = "` + otherTenant + `"
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = os.environ["KOODAM_APP_VERSION"].encode()
        if self.path.startswith("/model"):
            body = self.model().encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def model(self):
        data = json.dumps({"model": os.environ["KOODAM_GATEWAY_MODEL"], "input": "hi", "user": OTHER,
            "metadata": {"user_id": OTHER}, "litellm_metadata": {"user": OTHER},
            "safety_identifier": OTHER}).encode()
        with open(os.environ["KOODAM_GATEWAY_KEY_FILE"]) as f:
            key = f.read().strip()
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + key,
            "x-litellm-customer-id": OTHER, "x-litellm-end-user-id": OTHER}
        if "token" in self.path:
            headers["Koodam-Tenant-Token"] = self.headers.get("Koodam-Tenant-Token", "")
        req = urllib.request.Request(os.environ["KOODAM_GATEWAY_URL"] + "/v1/responses", data=data, headers=headers)
        # The gateway address is the egress proxy. The call uses no proxy variable.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(req, timeout=30) as r:
                return str(r.status)
        except urllib.error.HTTPError as e:
            return (str(e.code) + " " + (e.headers.get("Koodam-Reason") or "")).strip()
        except Exception as e:
            return "error " + type(e).__name__
    def log_message(self, *args):
        pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
`

// The thin App spec is the M2 exit test: an App serves 2 versions with a split, from the Helm
// install of the suite. That install has apps.enabled and no site input for the App. The spec
// adds the egress proxy and the claim check, and it gives back the chart of the suite at its end. Envoy
// Gateway is the Gateway API implementation of the test. Koodam does not depend on it.
var _ = Describe("Thin App", Ordered, Label("app"), func() {
	const (
		appNS = "koodam-e2e-app"
		// The pin of Envoy Gateway (Apache-2.0). Its chart has the Gateway API CRDs.
		egChart, egVersion, egNS = "oci://docker.io/envoyproxy/gateway-helm", "1.10.0", "envoy-gateway-system"
		gwName, appName, v1, v2  = "public", "shop", "shop-v1", "shop-v2"
		// static is a version with no model. It has no gateway key.
		static       = "shop-static"
		localURL     = "http://127.0.0.1:18080/"
		drainSeconds = 45
	)
	var fwd *exec.Cmd
	// token is the signed claim of each request of call. With no token, call sends no claim.
	var token string

	gatewayObject := func(kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetAPIVersion("gateway.networking.k8s.io/v1")
		u.SetKind(kind)
		u.SetNamespace(namespace)
		u.SetName(name)
		return u
	}
	version := func(name string) *corev1alpha1.AppVersion {
		return &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: appName}, BudgetPerDay: new(resource.MustParse("1")),
				Template: corev1alpha1.AppVersionTemplate{Model: "fake-responses",
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: pythonImage,
						Command: []string{"python", "-u", "-c", appServer},
						Ports:   []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("http")}}},
					}}}}},
			}}
	}
	weight := func(name string, w int32) corev1alpha1.AppEndpointVersion {
		return corev1alpha1.AppEndpointVersion{Name: name, Weight: &w}
	}
	endpoint := func() corev1alpha1.AppEndpointStatus {
		app := &corev1alpha1.App{}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: appName}, app); err != nil ||
			len(app.Status.Endpoints) != 1 {
			return corev1alpha1.AppEndpointStatus{}
		}
		return app.Status.Endpoints[0]
	}
	// call sends n requests through the Gateway and counts the answers of each version. With pin,
	// each request has the version header.
	call := func(n int, pin string) map[string]int {
		got := map[string]int{}
		for range n {
			req, err := http.NewRequest(http.MethodGet, localURL, nil)
			Expect(err).NotTo(HaveOccurred())
			if pin != "" {
				req.Header.Set(corev1alpha1.AppVersionHeader, pin)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				got["error"]++
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				got[fmt.Sprint(resp.StatusCode)]++
				continue
			}
			got[string(body)]++
		}
		return got
	}

	BeforeAll(func() {
		token = claimToken("e2e-client")
		// The claim check needs the egress proxy: then each version has a NetworkPolicy that
		// permits ingress from the namespace of the Envoy proxy to the check container only. The
		// cleanups run in the reverse order, so the chart of the suite comes back last.
		DeferCleanup(func() {
			By("installing the chart of the suite again")
			installChart()
		})
		// The chart makes a Role of the proxy in the namespace of the App, so the namespace is first.
		Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: appNS}}))).To(Succeed())
		proxy := filepath.Join(GinkgoT().TempDir(), "values-app-proxy.yaml")
		Expect(os.WriteFile(proxy, []byte(egressProxyValues()+fmt.Sprintf(
			"workspace:\n  networkPolicy:\n    namespaces: [%s]\napps:\n  ingressNamespaces: [%s]\n", appNS, egNS)),
			0o600)).To(Succeed())
		By("upgrading the chart with the egress proxy and the claim check")
		installChart("-f", proxy, "-f", claimValues())
		DeferCleanup(func() {
			if fwd != nil && fwd.Process != nil {
				_ = fwd.Process.Kill()
			}
			_ = k8s.DeleteAllOf(ctx, &corev1alpha1.AppVersion{}, client.InNamespace(appNS))
			Eventually(func() int {
				l := &corev1alpha1.AppVersionList{}
				_ = k8s.List(ctx, l, client.InNamespace(appNS))
				return len(l.Items)
			}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept an AppVersion.")
			_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNS}})
			_ = k8s.Delete(ctx, gatewayObject("GatewayClass", "", "koodam-e2e", nil))
			_, _ = utils.Run(exec.Command("helm", "uninstall", "eg", "-n", egNS, "--wait"))
		})

		By("installing Envoy Gateway " + egVersion)
		out, err := utils.Run(exec.Command("helm", "upgrade", "--install", "eg", egChart, "--version", egVersion,
			"-n", egNS, "--create-namespace", "--wait", "--timeout", "10m"))
		Expect(err).NotTo(HaveOccurred(), "Failed to install Envoy Gateway: "+out)

		By("making the Gateway, the App and 3 versions")
		twoModels, noModel := version(v2), version(static)
		twoModels.Spec.Template.Model, twoModels.Spec.Template.Models = "", []string{"fake-responses", "fake-responses-b"}
		noModel.Spec.Template.Model, noModel.Spec.BudgetPerDay = "", nil
		Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: appNS}}))).To(Succeed())
		for _, o := range []client.Object{
			gatewayObject("GatewayClass", "", "koodam-e2e", map[string]any{
				"controllerName": "gateway.envoyproxy.io/gatewayclass-controller"}),
			gatewayObject("Gateway", appNS, gwName, map[string]any{"gatewayClassName": "koodam-e2e",
				"listeners": []any{map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80)}}}),
			&corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: appNS},
				Spec: corev1alpha1.AppSpec{DrainSeconds: new(int32(drainSeconds)),
					Endpoints: []corev1alpha1.AppEndpoint{{Name: "web", Port: "http",
						Gateway:  corev1alpha1.AppGatewayReference{Name: gwName},
						Versions: []corev1alpha1.AppEndpointVersion{weight(v1, 80), weight(v2, 20), weight(static, 0)}}}}},
			version(v1), twoModels, noModel,
		} {
			Expect(k8s.Create(ctx, o)).To(Succeed())
		}
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"get", "apps,appversions,httproutes,gateways,deployments,pods,networkpolicies", "-n", appNS, "-o", "wide"},
			{"get", "apps,httproutes", "-n", appNS, "-o", "yaml"},
			{"logs", "-n", systemNS, "-l", "control-plane=controller-manager", "--tail", "100"},
		} {
			out, _ := utils.Run(exec.Command("kubectl", args...))
			_, _ = fmt.Fprintln(GinkgoWriter, out)
		}
	})

	It("serves 2 versions with the weights of the endpoint", func() {
		By("waiting for the route and the pods of the 2 versions")
		Eventually(func() string { return endpoint().RouteState }, 5*time.Minute, 2*time.Second).
			Should(Equal(corev1alpha1.AppRouteAccepted))
		// The route can be Accepted before a version has its key. Then the weight in effect is 0.
		Eventually(func() []corev1alpha1.AppEndpointVersion { return endpoint().Weights }, 5*time.Minute, 2*time.Second).
			Should(Equal([]corev1alpha1.AppEndpointVersion{weight(v1, 80), weight(v2, 20), weight(static, 0)}))
		for _, v := range []string{v1, v2, static} {
			_, err := utils.Run(exec.Command("kubectl", "-n", appNS, "rollout", "status", "deployment/kd-"+v,
				"--timeout", "5m"))
			Expect(err).NotTo(HaveOccurred(), "The pods of "+v+" did not start.")
		}

		By("refusing a template change on a version with a key")
		live := &corev1alpha1.AppVersion{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: v2}, live)).To(Succeed())
		live.Spec.Template.PodTemplate.Spec.Containers[0].Image = "registry.example.com/other:v1"
		Expect(apierrors.IsInvalid(k8s.Update(ctx, live))).To(BeTrue(), "The admission policy refuses the change.")

		By("reading the 2 models of one key, and no key for the version with no model")
		Expect(keyInfo(live.Status.KeyHash).Models).To(ConsistOf("fake-responses", "fake-responses-b"))
		keyless := &corev1alpha1.AppVersion{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: static}, keyless)).To(Succeed())
		Expect(keyless.Status.KeyAlias).To(BeEmpty())
		Expect(keyless.Status.KeyHash).To(BeEmpty())
		Expect(apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: "kd-" + static},
			&corev1.Secret{}))).To(BeTrue(), "A version with no model has no key Secret.")

		By("forwarding a local port to the Envoy proxy of the Gateway")
		var svc string
		Eventually(func() int {
			l := &corev1.ServiceList{}
			_ = k8s.List(ctx, l, client.InNamespace(egNS), client.MatchingLabels{
				"gateway.envoyproxy.io/owning-gateway-name": gwName, "gateway.envoyproxy.io/owning-gateway-namespace": appNS})
			if len(l.Items) == 1 {
				svc = l.Items[0].Name
			}
			return len(l.Items)
		}, 3*time.Minute, 2*time.Second).Should(Equal(1))
		// A port-forward to a proxy pod that is not ready stops, so wait for the proxy first.
		_, err := utils.Run(exec.Command("kubectl", "-n", egNS, "wait", "--for=condition=Available", "deployment",
			"-l", "gateway.envoyproxy.io/owning-gateway-name="+gwName, "--timeout", "5m"))
		Expect(err).NotTo(HaveOccurred(), "The Envoy proxy of the Gateway did not start.")
		fwd = exec.Command("kubectl", "-n", egNS, "port-forward", "svc/"+svc, "18080:80")
		fwd.Stdout, fwd.Stderr = GinkgoWriter, GinkgoWriter
		Expect(fwd.Start()).To(Succeed())
		Eventually(func() map[string]int {
			got := call(5, "")
			delete(got, v1)
			delete(got, v2)
			return got
		}, 3*time.Minute, 2*time.Second).Should(BeEmpty(), "Each answer comes from a version.")

		By("sending 200 requests with no header")
		got := call(200, "")
		Expect(got[v1]+got[v2]).To(Equal(200), "Each answer comes from a version: %v", got)
		// The weights are 80 and 20. The limits are wide, so that the test is stable.
		Expect(got[v2]).To(BeNumerically(">=", 10), "v2 gets a part of the traffic: %v", got)
		Expect(got[v1]).To(BeNumerically(">", got[v2]), "v1 gets the larger part: %v", got)

		By("refusing a request with no signed claim or with a claim of a different key")
		valid := token
		for _, bad := range []string{"", valid[:len(valid)-4] + "AAAA"} {
			token = bad
			Expect(call(5, "")).To(Equal(map[string]int{"401": 5}))
		}
		token = valid
		// The version has its NetworkPolicy, so the status says that the check protects it.
		Eventually(func() string {
			av := &corev1alpha1.AppVersion{}
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: v1}, av); err != nil {
				return err.Error()
			}
			if check := meta.FindStatusCondition(av.Status.Conditions, "ClaimCheck"); check != nil {
				return check.Reason
			}
			return ""
		}, time.Minute, 2*time.Second).Should(Equal("Enforced"))
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: "kd-" + v1},
			&networkingv1.NetworkPolicy{})).To(Succeed())

		By("keeping a session on one version with the header")
		Expect(call(30, v2)).To(Equal(map[string]int{v2: 30}))
		Expect(call(30, v1)).To(Equal(map[string]int{v1: 30}))
		// The version with no model has the weight 0, so a caller cannot select it with the header.
		Expect(call(10, static)).NotTo(HaveKey(static))

		By("giving a weight to the version with no model")
		app := &corev1alpha1.App{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: appName}, app)).To(Succeed())
		app.Spec.Endpoints[0].Versions = []corev1alpha1.AppEndpointVersion{weight(v1, 80), weight(v2, 20), weight(static, 1)}
		Expect(k8s.Update(ctx, app)).To(Succeed())
		Eventually(func() map[string]int { return call(10, static) }, time.Minute, time.Second).
			Should(Equal(map[string]int{static: 10}), "The version with no model serves with no gateway key.")
	})

	// The release workflow sets utils.SamplesEnv. The sample is the spans of the requests above,
	// as the check containers of the App pods wrote them. The drain of the next spec stops the
	// pods of v1, so this spec reads the spans first.
	It("writes the release sample of the App ingress spans", func() {
		dir := os.Getenv(utils.SamplesEnv)
		if dir == "" {
			Skip(utils.SamplesEnv + " is not set")
		}
		By("reading the spans of the check containers")
		out, err := exec.Command("kubectl", "-n", appNS, "logs", "-l", "koodam.dev/app="+appName,
			"-c", "koodam-claim", "--tail=-1", "--max-log-requests=10").Output()
		Expect(err).NotTo(HaveOccurred())
		// kubectl logs also gives the stderr of the container. Only a JSON line is a span.
		var lines []byte
		outcomes := map[string]bool{}
		for line := range strings.Lines(string(out)) {
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var sp appclaim.Span
			Expect(json.Unmarshal([]byte(line), &sp)).To(Succeed(), line)
			Expect(sp.V).To(Equal(appclaim.SpanVersion), "A span has no contract version: %s", line)
			Expect(sp.Kind).To(Equal(appclaim.KindIngress))
			outcomes[sp.Outcome] = true
			lines = append(lines, line...)
		}
		// The spec before sent requests with a valid claim, with no claim and with a bad signature.
		Expect(outcomes).To(HaveKey("ok"), "The spans have no request with a valid claim.")
		Expect(outcomes).To(HaveKey(string(appclaim.RefusedSyntax)), "The spans have no request with no claim.")
		Expect(outcomes).To(HaveKey(string(appclaim.RefusedSignature)), "The spans have no bad signature.")

		By("checking the sample for credentials and writing it")
		Expect(utils.WriteSamples(dir, map[string][]byte{utils.AppSpanSample: lines}, token, masterKey)).To(Succeed())
	})

	// The gateway must count a model call for the tenant of the request, not for a label that
	// the code in a pod sends (#415). The test is the proof for the pinned gateway image.
	It("counts a model call only on the tenant of the token", func() {
		// claimToken signs each claim for this tenant.
		const tenant, forger = "team-e2e", "tenant-forge"
		// get sends one request with the signed claim to the path of the version v1.
		get := func(path string) string {
			req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(localURL, "/")+path, nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set(corev1alpha1.AppVersionHeader, v1)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := httpClient.Do(req)
			if err != nil {
				return err.Error()
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			return fmt.Sprintf("%d %s", resp.StatusCode, body)
		}
		// count gives the successful model calls that the gateway has for a customer.
		count := func(customer string) int {
			const layout = "2006-01-02 15:04:05"
			q := url.Values{"end_user": {customer}, "status_filter": {"success"}, "page": {"1"}, "page_size": {"1"},
				"start_date": {time.Now().Add(-time.Hour).UTC().Format(layout)},
				"end_date":   {time.Now().Add(time.Hour).UTC().Format(layout)}}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/spend/logs/v2?"+q.Encode(), nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+masterKey)
			resp, err := httpClient.Do(req)
			if err != nil {
				return -1
			}
			defer func() { _ = resp.Body.Close() }()
			var logs struct {
				Total *int `json:"total"`
			}
			if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&logs) != nil || logs.Total == nil {
				return -1
			}
			return *logs.Total
		}

		By("making a model call from the app with the tenant token of the request")
		calls := 0
		Eventually(func() string {
			out := get("/model?token")
			if out == "200 200" {
				calls++
			}
			return out
		}, 2*time.Minute, 2*time.Second).Should(Equal("200 200"), "The model call with the tenant token did not work.")

		By("refusing a model call of the app with no tenant token")
		Expect(get("/model")).To(Equal("200 403 " + egress.ReasonTenantTokenMissing))

		By("reading the count of the gateway for the tenant of the token and for the tenant of the body")
		Eventually(func() int { return count(tenant) }, 3*time.Minute, 2*time.Second).Should(Equal(calls),
			"The gateway did not count the model call on the tenant of the token.")
		Expect(count(otherTenant)).To(BeZero(), "The gateway counted a call on the tenant of the body.")

		By("making a model call from an Agent pod with the label of the tenant")
		a := sampleAgent(forger, "sleep 900")
		a.Namespace = appNS
		Expect(k8s.Create(ctx, a)).To(Succeed())
		DeferCleanup(func() { _ = k8s.Delete(ctx, a) })
		pod := ""
		Eventually(func(g Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8s.List(ctx, pods, client.InNamespace(appNS), client.MatchingLabels{agentLabel: forger})).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Status.Phase).To(Equal(corev1.PodRunning))
			pod = pods.Items[0].Name
		}, 3*time.Minute, time.Second).Should(Succeed(), "The Agent pod does not run.")
		curl := `curl -sS -o /dev/null -w '%{http_code}' --max-time 30 -H "Authorization: Bearer $OPENAI_API_KEY" ` +
			`-H 'Content-Type: application/json' -H 'x-litellm-customer-id: ` + tenant + `' ` +
			`-d '{"model":"` + mockModel + `","messages":[{"role":"user","content":"hi"}],"user":"` + tenant + `"}' ` +
			`"$OPENAI_BASE_URL/chat/completions"`
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "exec", "-n", appNS, pod, "--", "sh", "-c", curl))
			return out
		}, time.Minute, 2*time.Second).Should(Equal("200"), "The model call of the Agent pod did not work.")
		Eventually(func() int { return count(egress.NoTenantCustomer) }, 3*time.Minute, 2*time.Second).
			Should(BeNumerically(">=", 1), "The gateway did not count the Agent call on the reserved customer.")
		Expect(count(tenant)).To(Equal(calls), "The gateway counted the Agent call on the tenant.")

		By("refusing the gateway key of the App in a model call from the Agent pod")
		appKey := &corev1.Secret{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: "kd-" + v1}, appKey)).To(Succeed())
		// The key goes to the pod on the standard input. Thus no command line and no log has it.
		relay := `read -r key; curl -sS -o /dev/null -D - --max-time 30 -H "Authorization: Bearer $key" ` +
			`-H 'Content-Type: application/json' ` +
			`-d '{"model":"` + mockModel + `","messages":[{"role":"user","content":"hi"}]}' ` +
			`"$OPENAI_BASE_URL/chat/completions" | tr -d '\r' | grep -i -e '^HTTP/' -e '^koodam-reason:'`
		cmd := exec.Command("kubectl", "exec", "-i", "-n", appNS, pod, "--", "sh", "-c", relay)
		cmd.Stdin = strings.NewReader(string(appKey.Data["key"]) + "\n")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred(), "The model call with the key of the App did not run.")
		Expect(string(out)).To(And(ContainSubstring(" 403"), ContainSubstring(egress.ReasonAppKeyOutOfPod)),
			"The proxy did not refuse the key of the App from the Agent pod.")
	})

	It("drains a retiring version and then revokes its key", func() {
		av := &corev1alpha1.AppVersion{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: v1}, av)).To(Succeed())
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8s.Update(ctx, av)).To(Succeed())

		By("sending each new session to the other version")
		Eventually(func() []corev1alpha1.AppEndpointVersion { return endpoint().Weights }, time.Minute, time.Second).
			Should(Equal([]corev1alpha1.AppEndpointVersion{weight(v1, 0), weight(v2, 20), weight(static, 1)}))
		// Each answer comes from a version that stays, so an error or a 503 fails the test.
		rest := func() int {
			got := call(20, "")
			return got[v2] + got[static]
		}
		Eventually(rest, time.Minute, time.Second).Should(Equal(20))

		By("revoking the key after the drain time")
		Eventually(func() bool {
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: v1}, av); err != nil {
				return false
			}
			return meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionRevoked)
		}, drainSeconds*time.Second+3*time.Minute, 2*time.Second).Should(BeTrue())
		Expect(meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionSessionsEnded)).To(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: "kd-" + v1},
				&appsv1.Deployment{}))
		}, time.Minute, time.Second).Should(BeTrue())
		// The other versions serve all the traffic.
		Expect(rest()).To(Equal(20))
	})
})
