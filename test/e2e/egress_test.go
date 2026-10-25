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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/egress"
	"github.com/mohanraj00/koodam/test/utils"
)

// The traced egress spec upgrades the chart with the egress proxy for the namespace egNS. An Agent
// pod and a Workspace pod make planted calls, and each call must have 1 span with the identity of
// the pod. The bypass attempts must fail. At the end, the spec installs the chart of the suite
// again. kindnet enforces NetworkPolicy.
var _ = Describe("Traced egress", Ordered, Label("egress"), func() {
	const (
		egNS, egProfile = "koodam-e2e-egress", "koodam-e2e-egress"
		agentName       = "egress-run"
		wsTraceName     = "egress-ws"
		// proxyIP is a fixed ClusterIP in the Service range of kind (10.96.0.0/16).
		proxyIP   = "10.96.100.100"
		proxyAddr = proxyIP + ":3128"
		// upstream is the fake upstream Service. The values give it to extraEgress.
		upstream = fakeUpstream + "." + systemNS + ".svc.cluster.local"
	)
	var koodam, agentPod, wsPod, runID string

	// kexec runs a shell command in the first container of pod in egNS.
	kexec := func(pod, shell, command string) (string, error) {
		return utils.Run(exec.Command("kubectl", "exec", "-n", egNS, pod, "--", shell, "-c", command))
	}
	// reach is true if pod opens a TCP connection to host/port in 5 seconds. The Workspace image
	// has bash and no nc. The curl image of the other pods has the busybox nc.
	reach := func(pod, addr string) bool {
		if pod == wsPod {
			_, err := kexec(pod, "bash", `timeout 5 bash -c 'exec 3<>"/dev/tcp/`+addr+`"'`)
			return err == nil
		}
		host, port, _ := strings.Cut(addr, "/")
		_, err := kexec(pod, "sh", "timeout 8 nc -z -w 5 "+host+" "+port)
		return err == nil
	}
	// dnsUDP is true if pod gets an answer to a UDP DNS query for example.com from server in 5 seconds.
	dnsUDP := func(pod, server string) bool {
		if pod == wsPod {
			// A DNS query for the A record of example.com, with the ID 0x1234.
			const query = `\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01`
			out, err := kexec(pod, "bash", `exec 3<>/dev/udp/`+server+`/53 && printf '`+query+`' >&3 && `+
				`timeout 5 head -c 12 <&3 | wc -c`)
			return err == nil && strings.TrimSpace(out) == "12"
		}
		_, err := kexec(pod, "sh", "timeout 8 nslookup example.com "+server)
		return err == nil
	}
	// spans returns the spans of target from koodam trace --json.
	spans := func(target string) []egress.Span {
		out, err := utils.Run(exec.Command(koodam, "-n", egNS, "trace", target, "--json",
			"--proxy-namespace", systemNS))
		Expect(err).NotTo(HaveOccurred(), out)
		var list []egress.Span
		for line := range strings.Lines(strings.TrimSpace(out)) {
			var sp egress.Span
			Expect(json.Unmarshal([]byte(line), &sp)).To(Succeed(), line)
			list = append(list, sp)
		}
		return list
	}
	// count returns the spans of the kind with the host or the SNI.
	count := func(list []egress.Span, kind, host string) []egress.Span {
		var got []egress.Span
		for _, sp := range list {
			if sp.Kind == kind && (sp.Host == host || sp.SNI == host) {
				got = append(got, sp)
			}
		}
		return got
	}

	BeforeAll(func() {
		koodam = filepath.Join(GinkgoT().TempDir(), "koodam")
		_, err := utils.Run(exec.Command("go", "build", "-o", koodam, "./cmd/koodam"))
		Expect(err).NotTo(HaveOccurred())

		By("finding the API server endpoint for the proxy NetworkPolicy")
		eps := &discoveryv1.EndpointSliceList{}
		Expect(k8s.List(ctx, eps, client.InNamespace("default"),
			client.MatchingLabels{"kubernetes.io/service-name": "kubernetes"})).To(Succeed())
		Expect(eps.Items).NotTo(BeEmpty())
		ep := eps.Items[0]
		Expect(ep.Endpoints).NotTo(BeEmpty())
		Expect(ep.Ports).NotTo(BeEmpty())
		values := fmt.Sprintf(`egressProxy:
  enabled: true
  clusterIP: %s
  replicas: 1
  networkPolicy:
    apiServer:
    - to:
      - ipBlock:
          cidr: %s/32
      ports:
      - protocol: TCP
        port: %d
workspace:
  networkPolicy:
    namespaces: [%s]
    extraEgress:
    - to:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: %s
        podSelector:
          matchLabels:
            app: %s
      ports:
      - protocol: TCP
        port: 8080
`, proxyIP, ep.Endpoints[0].Addresses[0], *ep.Ports[0].Port, egNS, systemNS, fakeUpstream)
		file := filepath.Join(GinkgoT().TempDir(), "values-egress.yaml")
		Expect(os.WriteFile(file, []byte(values), 0o600)).To(Succeed())

		// The cleanups run in the reverse order: the namespace goes first, then the chart of the
		// suite comes back.
		DeferCleanup(func() {
			By("installing the chart of the suite again")
			installChart()
		})
		DeferCleanup(func() {
			_ = k8s.Delete(ctx, &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: egProfile}})
			_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: egNS}})
			Eventually(func() error {
				return k8s.Get(ctx, client.ObjectKey{Name: egNS}, &corev1.Namespace{})
			}, 5*time.Minute, 2*time.Second).ShouldNot(Succeed(), "The namespace "+egNS+" was not deleted.")
		})
		for _, o := range []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: egNS,
				Labels: map[string]string{corev1alpha1.ProfileLabel: egProfile}}},
			&corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: egProfile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{egNS}}},
		} {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}

		By("upgrading the chart with the egress proxy")
		installChart("-f", file)

		By("making an Agent and a Workspace that use the proxy")
		a := sampleAgent(agentName, "sleep 900")
		a.Namespace = egNS
		Expect(k8s.Create(ctx, a)).To(Succeed())
		ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: wsTraceName, Namespace: egNS},
			Spec: corev1alpha1.WorkspaceSpec{Image: workspaceImage, Storage: resource.MustParse("1Gi")}}
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		waitPhase(ws, corev1alpha1.WorkspaceRunning)
		wsPod = wsTraceName + "-0"
		Eventually(func(g Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8s.List(ctx, pods, client.InNamespace(egNS),
				client.MatchingLabels{agentLabel: agentName})).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Status.Phase).To(Equal(corev1.PodRunning))
			g.Expect(pods.Items[0].Spec.DNSConfig).NotTo(BeNil())
			g.Expect(pods.Items[0].Spec.DNSConfig.Nameservers).To(Equal([]string{proxyIP}))
			agentPod, runID = pods.Items[0].Name, pods.Items[0].Labels[egress.RunLabel]
		}, 3*time.Minute, time.Second).Should(Succeed(), "The Agent pod does not run with the proxy DNS.")
	})

	It("traces the planted calls of an Agent and a Workspace with the pod identity", func() {
		By("making the planted calls from the Agent pod")
		curl := `curl -sS -o /dev/null -w '%{http_code}' --max-time 10 `
		for _, c := range []string{
			curl + "https://example.com/",
			curl + "-x http://" + proxyAddr + " https://example.org/",
			// The claim header is not the identity. The span has the run of the source pod.
			curl + "-H 'X-Koodam-Run: spoofed' -H 'X-Koodam-User: spoofed' http://example.net/",
			curl + `-H "Authorization: Bearer $OPENAI_API_KEY" -H 'Content-Type: application/json' ` +
				`-d '{"model":"` + mockModel + `","messages":[{"role":"user","content":"hi"}]}' ` +
				`"$OPENAI_BASE_URL/chat/completions"`,
		} {
			out, err := kexec(agentPod, "sh", c)
			Expect(err).NotTo(HaveOccurred(), c+": "+out)
			Expect(out).To(Equal("200"), c)
		}

		By("making the planted calls from the Workspace pod")
		out, err := kexec(wsPod, "bash", `exec 3<>/dev/tcp/example.com/80 && `+
			`printf 'GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n' >&3 && head -1 <&3`)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(HavePrefix("HTTP/1.1 200"))
		out, err = kexec(wsPod, "bash", "HOME=/tmp timeout 30 git ls-remote https://github.com/mohanraj00/koodam HEAD")
		Expect(err).NotTo(HaveOccurred(), out)

		By("checking the K4 calls of the Workspace pod that have no span")
		Expect(reach(wsPod, "127.0.0.1/2222")).To(BeTrue(), "The Workspace pod cannot reach its own sshd.")
		Expect(reach(wsPod, upstream+"/8080")).To(BeTrue(), "The Workspace pod cannot reach the extraEgress Service.")

		By("reading the spans with koodam trace")
		Eventually(func(g Gomega) {
			list := spans("agent/" + agentName)
			for _, want := range []struct{ kind, host string }{
				{egress.KindTLS, "example.com"}, {egress.KindConnect, "example.org"},
				{egress.KindHTTP, "example.net"}, {egress.KindLLM, ""},
			} {
				got := count(list, want.kind, want.host)
				g.Expect(got).To(HaveLen(1), "Agent spans of %s %s: %v", want.kind, want.host, got)
				sp := got[0]
				g.Expect(sp.V).To(Equal(egress.SpanVersion))
				g.Expect(sp.Port).NotTo(BeEmpty(), "The span has no port.")
				g.Expect(sp.Result).To(Equal(egress.ResultOK))
				g.Expect(sp.Agent).To(Equal(agentName))
				g.Expect(sp.Run).To(Equal(runID), "The span does not have the run of the source pod.")
				g.Expect(sp.Pod).To(Equal(agentPod))
			}
			g.Expect(count(list, egress.KindHTTP, "example.net")[0].Claimed).To(BeTrue())

			list = spans("workspace/" + wsTraceName)
			for _, want := range []struct{ kind, host string }{
				{egress.KindHTTP, "example.com"}, {egress.KindTLS, "github.com"},
			} {
				got := count(list, want.kind, want.host)
				g.Expect(got).To(HaveLen(1), "Workspace spans of %s %s: %v", want.kind, want.host, got)
				g.Expect(got[0].V).To(Equal(egress.SpanVersion))
				g.Expect(got[0].Port).NotTo(BeEmpty(), "The span has no port.")
				g.Expect(got[0].Workspace).To(Equal(wsTraceName))
				g.Expect(got[0].Pod).To(Equal(wsPod))
			}
			for _, sp := range list {
				g.Expect(sp.Host).NotTo(Or(Equal("127.0.0.1"), HavePrefix(fakeUpstream)), "%v", sp)
			}
		}, time.Minute, 2*time.Second).Should(Succeed())
	})

	It("blocks each call that does not go through the proxy", func() {
		podIP := func(ns, label, value string) string {
			pods := &corev1.PodList{}
			Expect(k8s.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels{label: value})).To(Succeed())
			Expect(pods.Items).NotTo(BeEmpty(), "There is no pod with "+label+"="+value+".")
			return pods.Items[0].Status.PodIP
		}
		svc := &corev1.Service{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: systemNS, Name: release + "-litellm"}, svc)).To(Succeed())

		// The control pod has no Koodam label, so no NetworkPolicy selects it and it uses the cluster
		// DNS. Each attempt must work from it, so that a blocked attempt shows the policy of the source.
		By("starting a control pod with a listener, for the same-namespace attempt")
		const control = "egress-control"
		Expect(k8s.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: control, Namespace: egNS, Labels: map[string]string{"app": control}},
			Spec: corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: new(true), RunAsUser: new(int64(100)),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers: []corev1.Container{{Name: "control", Image: curlImage,
					Command: []string{"sh", "-c", "nc -lk -p 9000 -e /bin/cat"},
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: new(false),
						Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}},
			},
		})).To(Succeed())
		var controlIP string
		Eventually(func(g Gomega) {
			controlIP = podIP(egNS, "app", control)
			g.Expect(controlIP).NotTo(BeEmpty())
			g.Expect(reach(control, controlIP+"/9000")).To(BeTrue())
		}, 3*time.Minute, 2*time.Second).Should(Succeed(), "The listener of the control pod does not answer.")

		// The proxy DNS gives the proxy IP for each external name, so the port 22 attempt uses an IP.
		ghIP, err := kexec(control, "sh", `curl -sS -o /dev/null -w '%{remote_ip}' --max-time 10 https://github.com/`)
		Expect(err).NotTo(HaveOccurred(), ghIP)

		attempts := []struct{ what, addr string }{
			{"a direct HTTPS call to an IP address", "1.1.1.1/443"},
			{"a direct TCP DNS query", "1.1.1.1/53"},
			{"a direct call to an external IP on port 22", ghIP + "/22"},
			{"a direct gateway pod call", podIP(systemNS, "app.kubernetes.io/component", "litellm") + "/4000"},
			{"a direct gateway Service call", svc.Spec.ClusterIP + "/4000"},
			{"a pod in the same namespace", controlIP + "/9000"},
		}
		By("checking that each attempt works from the control pod")
		for _, a := range attempts {
			Expect(reach(control, a.addr)).To(BeTrue(), "The control pod cannot make "+a.what+": "+a.addr)
		}
		Expect(dnsUDP(control, "1.1.1.1")).To(BeTrue(), "The control pod cannot make a UDP DNS query.")

		for _, pod := range []string{wsPod, agentPod} {
			By("checking that each attempt fails from " + pod)
			for _, a := range attempts {
				Expect(reach(pod, a.addr)).To(BeFalse(), pod+" can make "+a.what+": "+a.addr)
			}
			Expect(dnsUDP(pod, "1.1.1.1")).To(BeFalse(), pod+" can make a direct UDP DNS query.")
		}

		By("checking that the proxy refuses a CONNECT to port 22")
		// curl fails after the refused CONNECT, so "|| true" keeps the kubectl exit message out of the output.
		out, _ := kexec(agentPod, "sh", `curl -sS -o /dev/null -w '%{http_connect}' --max-time 10 `+
			`-x http://`+proxyAddr+` https://example.com:22/ 2>/dev/null || true`)
		Expect(strings.TrimSpace(out)).To(Equal("403"), "The proxy does not refuse a CONNECT to port 22.")
	})
})
