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
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/test/utils"
)

// appServer is the app of the test. It answers each GET with the name of its AppVersion.
const appServer = `
import http.server, os
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = os.environ["KOODAM_APP_VERSION"].encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
`

// The thin App spec is the M2 exit test: an App serves 2 versions with a split, from the Helm
// install of the suite. That install has apps.enabled and no site input for the App. Envoy
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
		// The suite has no egress proxy, so the version has no NetworkPolicy. The status says
		// that a pod of the cluster can call the app container directly.
		av := &corev1alpha1.AppVersion{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: appNS, Name: v1}, av)).To(Succeed())
		check := meta.FindStatusCondition(av.Status.Conditions, "ClaimCheck")
		Expect(check).NotTo(BeNil())
		Expect(check.Reason).To(Equal("NoNetworkPolicy"))

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
