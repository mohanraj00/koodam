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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/test/utils"
)

// gateApp is the app of the gate test. It answers a POST with the message of the request, so 2
// different messages get 2 different replies.
const gateApp = `
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def answer(self, body):
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        self.answer(b"ready")
    def do_POST(self):
        self.answer(b"You said: " + self.rfile.read(int(self.headers.get("Content-Length", 0))))
    def log_message(self, *args):
        pass
http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
`

// The evidence gate spec is the kind test of docs/evidence-gate.md. A gate image with the pinned
// Nookku release runs one gate that fails and one gate that passes. A rollout stops on the failed
// verdict and goes on with the pass. The spec uses the chart of the suite: no egress proxy and no
// claim check. It needs only the HTTPRoute CRD of the Gateway API, and no Gateway controller.
var _ = Describe("Evidence gate", Ordered, Label("gate"), func() {
	const (
		gateNS    = "koodam-e2e-gate"
		gateImage = "example.com/koodam-e2e-gate:pin"
		// The chart of Envoy Gateway (Apache-2.0) has the Gateway API CRDs. The spec applies only its CRDs.
		crdChart, crdVersion      = "oci://docker.io/envoyproxy/gateway-helm", "1.10.0"
		appName, live, bad, good  = "gated", "gated-v1", "gated-fail", "gated-pass"
		gateTimeout, gateInterval = 6 * time.Minute, 2 * time.Second
		// gatePodLabel is the label of the gate pod, with the name of its version.
		gatePodLabel = "koodam.dev/app-gate"
		// sessionRules is the key of routeWeights for the count of the session rules of the route.
		sessionRules = "session rules"
	)
	var (
		// appImage is pythonImage by digest. A gated version must name its image by digest.
		appImage string
		// nookkuVersion is the version of the pin.
		nookkuVersion string
	)

	version := func(name, gateFile string) *corev1beta1.AppVersion {
		av := &corev1beta1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateNS},
			Spec: corev1beta1.AppVersionSpec{AppRef: corev1beta1.AppReference{Name: appName},
				Template: corev1beta1.AppVersionTemplate{PodTemplate: corev1beta1.PodTemplate{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: corev1beta1.AppContainerName, Image: appImage,
						Command: []string{"python", "-u", "-c", gateApp},
						Ports:   []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("http")}}},
					}}}}}}}
		if gateFile != "" {
			av.Spec.Gate = &corev1beta1.AppVersionGate{Nookku: corev1beta1.NookkuGate{
				Image: gateImage, Root: "/project", GateFile: gateFile, DeadlineSeconds: new(int32(300))}}
		}
		return av
	}
	weight := func(name string, w int32) corev1beta1.AppEndpointVersion {
		return corev1beta1.AppEndpointVersion{Name: name, Weight: &w}
	}
	// setWeights writes the weights of the endpoint to the spec of the App.
	setWeights := func(versions ...corev1beta1.AppEndpointVersion) {
		GinkgoHelper()
		Eventually(func() error {
			app := &corev1beta1.App{}
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: gateNS, Name: appName}, app); err != nil {
				return err
			}
			app.Spec.Endpoints[0].Versions = versions
			return k8s.Update(ctx, app)
		}, time.Minute, time.Second).Should(Succeed())
	}
	// inEffect gives back the weights in effect of the endpoint, from the status of the App.
	inEffect := func() []corev1beta1.AppEndpointVersion {
		app := &corev1beta1.App{}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: gateNS, Name: appName}, app); err != nil ||
			len(app.Status.Endpoints) != 1 {
			return nil
		}
		return app.Status.Endpoints[0].Weights
	}
	// routeWeights gives back the weight of each backend Service in the weight rule of the HTTPRoute.
	routeWeights := func() map[string]int64 {
		route := &unstructured.Unstructured{}
		route.SetAPIVersion("gateway.networking.k8s.io/v1")
		route.SetKind("HTTPRoute")
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: gateNS, Name: "kd-" + appName + ".web"}, route); err != nil {
			return nil
		}
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
		got := map[string]int64{}
		if len(rules) == 0 {
			return got
		}
		// The weight rule is the last rule. Each rule before it is the session rule of one version.
		refs, _, _ := unstructured.NestedSlice(rules[len(rules)-1].(map[string]any), "backendRefs")
		for _, r := range refs {
			ref := r.(map[string]any)
			got[ref["name"].(string)], _ = ref["weight"].(int64)
		}
		got[sessionRules] = int64(len(rules) - 1)
		return got
	}
	// evidence waits for the end state of the gate of a version and gives back the version.
	evidence := func(name string, status metav1.ConditionStatus, reason string) *corev1beta1.AppVersion {
		GinkgoHelper()
		av := &corev1beta1.AppVersion{}
		Eventually(func() string {
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: gateNS, Name: name}, av); err != nil {
				return err.Error()
			}
			c := meta.FindStatusCondition(av.Status.Conditions, corev1beta1.AppVersionEvidence)
			if c == nil || c.Status == metav1.ConditionUnknown {
				return "the gate has no end state"
			}
			return string(c.Status) + "/" + c.Reason
		}, gateTimeout, gateInterval).Should(Equal(string(status) + "/" + reason))
		return av
	}

	BeforeAll(func() {
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())

		By("building the gate image with the pinned Nookku release and loading it on kind")
		pin, err := os.ReadFile(filepath.Join(dir, "internal", "evidence", "nookku.pin"))
		Expect(err).NotTo(HaveOccurred())
		value := func(key, form string) string {
			m := regexp.MustCompile(`(?m)^` + key + `=(` + form + `)$`).FindSubmatch(pin)
			Expect(m).NotTo(BeNil(), "nookku.pin has no valid "+key)
			return string(m[1])
		}
		nookkuVersion = value("NOOKKU_VERSION", `[0-9.]+`)
		out, err := utils.Run(exec.Command("docker", "build", "-t", gateImage, "--build-arg", "BASE="+pythonImage,
			"--build-arg", "NOOKKU_VERSION="+nookkuVersion,
			"--build-arg", "NOOKKU_WHEEL_SHA256="+value("NOOKKU_WHEEL_SHA256", `[0-9a-f]{64}`),
			filepath.Join(dir, "test", "e2e", "testdata", "gate")))
		Expect(err).NotTo(HaveOccurred(), "Failed to build the gate image: "+out)
		Expect(utils.LoadImageToKindClusterWithName(gateImage)).To(Succeed())

		By("reading the digest of the app image")
		_, err = utils.Run(exec.Command("docker", "pull", pythonImage))
		Expect(err).NotTo(HaveOccurred())
		out, err = utils.Run(exec.Command("docker", "inspect", "--format", "{{index .RepoDigests 0}}", pythonImage))
		Expect(err).NotTo(HaveOccurred())
		_, digest, found := strings.Cut(strings.TrimSpace(out), "@")
		Expect(found).To(BeTrue(), "The app image has no repo digest: "+out)
		appImage = pythonImage[:strings.LastIndex(pythonImage, ":")] + "@" + digest

		By("applying the Gateway API CRDs if the cluster does not have the HTTPRoute")
		if _, err := utils.Run(exec.Command("kubectl", "get", "crd", "httproutes.gateway.networking.k8s.io")); err != nil {
			shown, err := exec.Command("helm", "show", "crds", crdChart, "--version", crdVersion).Output()
			Expect(err).NotTo(HaveOccurred(), "Failed to read the CRDs of the chart.")
			// helm writes the pull messages of an OCI chart to stdout, before the first YAML document.
			// kubectl refuses that text, so the spec applies only the text after the first separator.
			_, crds, found := bytes.Cut(append([]byte("\n"), shown...), []byte("\n---\n"))
			Expect(found).To(BeTrue(), "The output of helm has no YAML document.")
			apply := exec.Command("kubectl", "apply", "--server-side", "-f", "-")
			apply.Stdin = bytes.NewReader(crds)
			out, err := utils.Run(apply)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply the Gateway API CRDs: "+out)
		}

		By("making the App and its first version, with no gate")
		Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: gateNS}}))).To(Succeed())
		DeferCleanup(func() {
			_ = k8s.DeleteAllOf(ctx, &corev1beta1.AppVersion{}, client.InNamespace(gateNS))
			Eventually(func() int {
				l := &corev1beta1.AppVersionList{}
				_ = k8s.List(ctx, l, client.InNamespace(gateNS))
				return len(l.Items)
			}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept an AppVersion.")
			_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gateNS}})
		})
		// The Gateway of the endpoint does not exist. The spec reads only the backends of the route.
		Expect(k8s.Create(ctx, &corev1beta1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: gateNS},
			Spec: corev1beta1.AppSpec{Endpoints: []corev1beta1.AppEndpoint{{Name: "web", Port: "http",
				Gateway:  corev1beta1.AppGatewayReference{Name: "public"},
				Versions: []corev1beta1.AppEndpointVersion{weight(live, 100)}}}}})).To(Succeed())
		Expect(k8s.Create(ctx, version(live, ""))).To(Succeed())
		Eventually(routeWeights, gateTimeout, gateInterval).Should(Equal(
			map[string]int64{"kd-" + live: 100, sessionRules: 1}))
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"get", "apps,appversions,httproutes,deployments,jobs,pods,networkpolicies", "-n", gateNS, "-o", "wide"},
			{"get", "appversions,httproutes", "-n", gateNS, "-o", "yaml"},
			{"describe", "pods", "-n", gateNS, "-l", gatePodLabel},
			{"logs", "-n", gateNS, "-l", gatePodLabel, "--all-containers", "--tail", "60"},
			{"logs", "-n", systemNS, "-l", "control-plane=controller-manager", "--tail", "100"},
		} {
			out, _ := utils.Run(exec.Command("kubectl", args...))
			_, _ = fmt.Fprintln(GinkgoWriter, out)
		}
	})

	It("stops a rollout on a failed verdict", func() {
		By("adding a version with a gate that fails, with half of the traffic in the spec")
		Expect(k8s.Create(ctx, version(bad, "gates/fail.json"))).To(Succeed())
		setWeights(weight(live, 50), weight(bad, 50))

		av := evidence(bad, metav1.ConditionFalse, corev1beta1.EvidenceFailed)
		Expect(av.Status.Evidence).NotTo(BeNil())
		Expect(av.Status.Evidence.Verdict).To(Equal(corev1beta1.EvidenceFail))
		Expect(av.Status.Evidence.NookkuVersion).To(Equal(nookkuVersion))

		By("reading the weights: the old version keeps its traffic, and the new version gets none")
		Eventually(inEffect, gateTimeout, gateInterval).Should(Equal(
			[]corev1beta1.AppEndpointVersion{weight(live, 50), weight(bad, 0)}))
		Expect(routeWeights()).To(Equal(map[string]int64{"kd-" + live: 50, sessionRules: 1}))
		Consistently(routeWeights, 10*time.Second, gateInterval).Should(Equal(
			map[string]int64{"kd-" + live: 50, sessionRules: 1}))
	})

	It("goes on with a rollout after a pass", func() {
		By("adding a version with a gate that passes")
		Expect(k8s.Create(ctx, version(good, "gates/pass.json"))).To(Succeed())
		setWeights(weight(live, 50), weight(bad, 50), weight(good, 50))

		av := evidence(good, metav1.ConditionTrue, corev1beta1.EvidencePassed)
		digest := appImage[strings.LastIndex(appImage, "@")+1:]
		rec := av.Status.Evidence
		Expect(rec).NotTo(BeNil())
		Expect(rec.Subject).To(Equal(fmt.Sprintf("koodam.dev/appversion/v1:%s/%s:%s:%s", gateNS, good, av.UID, digest)))
		Expect([]string{string(rec.Source), string(rec.Verdict), string(rec.Integrity), rec.NookkuVersion}).To(Equal(
			[]string{"nookku-gate", "pass", "seal", nookkuVersion}))
		Expect(rec.ID).To(MatchRegexp(`^gate-[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`))
		Expect(rec.Digest).To(MatchRegexp(`^sha256:[0-9a-f]{64}$`))

		By("reading the weights: the new version gets the weight of the spec")
		Eventually(inEffect, gateTimeout, gateInterval).Should(Equal(
			[]corev1beta1.AppEndpointVersion{weight(live, 50), weight(bad, 0), weight(good, 50)}))
		Expect(routeWeights()).To(Equal(map[string]int64{"kd-" + live: 50, "kd-" + good: 50, sessionRules: 2}))

		By("reading the gate pod: no ServiceAccount token, and a NetworkPolicy of its own")
		pods := &corev1.PodList{}
		Expect(k8s.List(ctx, pods, client.InNamespace(gateNS),
			client.MatchingLabels{gatePodLabel: good})).To(Succeed())
		Expect(pods.Items).To(HaveLen(1))
		Expect(pods.Items[0].Spec.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		Expect(pods.Items[0].Spec.Volumes).To(HaveLen(2))
		np := &unstructured.Unstructured{}
		np.SetAPIVersion("networking.k8s.io/v1")
		np.SetKind("NetworkPolicy")
		err := k8s.Get(ctx, client.ObjectKey{Namespace: gateNS, Name: "kd-" + good + ".gate"}, np)
		Expect(apierrors.IsNotFound(err)).To(BeFalse(), "The gate pod has no NetworkPolicy.")
		Expect(err).NotTo(HaveOccurred())
	})
})
