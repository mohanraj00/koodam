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
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/test/utils"
)

// The NetworkPolicy spec applies the chart policy to a probe pod with the Workspace label. It
// checks each target before and after the apply, so a block comes from the policy. kindnet
// enforces NetworkPolicy.
var _ = Describe("Workspace NetworkPolicy", Ordered, Label("workspace"), func() {
	const npNS, probe = "koodam-e2e-np", "probe"

	// reach is true if the probe pod opens a TCP connection to addr in 5 seconds.
	reach := func(addr string) bool {
		_, err := utils.Run(exec.Command("kubectl", "exec", "-n", npNS, probe, "--",
			"timeout", "5", "bash", "-c", `exec 3<>"/dev/tcp/$1"`, "--", addr))
		return err == nil
	}
	// podAddr returns the IP and the port of the gateway pod with the component label c.
	podAddr := func(c, port string) string {
		pods := &corev1.PodList{}
		Expect(k8s.List(ctx, pods, client.InNamespace(systemNS),
			client.MatchingLabels{"app.kubernetes.io/component": c})).To(Succeed())
		Expect(pods.Items).NotTo(BeEmpty(), "There is no "+c+" pod.")
		return pods.Items[0].Status.PodIP + "/" + port
	}

	BeforeAll(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: npNS}}
		Expect(k8s.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8s.Delete(ctx, ns))).To(Succeed()) })
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: probe, Namespace: npNS,
			Labels: map[string]string{"koodam.dev/workspace": probe}},
			Spec: corev1.PodSpec{
				AutomountServiceAccountToken: new(false),
				Containers: []corev1.Container{{Name: probe, Image: workspaceImage,
					ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sleep", "infinity"}}},
			}}
		Expect(k8s.Create(ctx, pod)).To(Succeed())
		_, err := utils.Run(exec.Command("kubectl", "wait", "-n", npNS, "pod/"+probe,
			"--for=condition=Ready", "--timeout=2m"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("lets a Workspace pod reach only DNS, the gateway and the public internet", func() {
		gw, db := podAddr("litellm", "4000"), podAddr("postgres", "5432")
		svcAddr := func(ns, name, port string) string {
			svc := &corev1.Service{}
			Expect(k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, svc)).To(Succeed())
			return svc.Spec.ClusterIP + "/" + port
		}
		apiAddr, gwSvc := svcAddr("default", "kubernetes", "443"), svcAddr(systemNS, release+"-litellm", "4000")
		// public is a public address on a permitted port. dnsOut is a public DNS server.
		const public, dnsOut = "1.1.1.1/443", "1.1.1.1/53"

		By("reaching each target before the policy")
		for _, a := range []string{gw, gwSvc, db, apiAddr, public, dnsOut} {
			Expect(reach(a)).To(BeTrue(), "The probe cannot reach "+a+" with no policy.")
		}

		By("applying the chart policy to the namespace")
		out, err := utils.Run(exec.Command("helm", "template", release, "charts/koodam", "-n", systemNS,
			"-f", "charts/koodam/values-bundled.yaml", "-f", "test/e2e/testdata/values-e2e.yaml",
			"--set", "workspace.networkPolicy.namespaces={"+npNS+"}",
			"--show-only", "templates/workspace-networkpolicy.yaml"))
		Expect(err).NotTo(HaveOccurred(), out)
		file := filepath.Join(GinkgoT().TempDir(), "np.yaml")
		Expect(os.WriteFile(file, []byte(out), 0o600)).To(Succeed())
		out, err = utils.Run(exec.Command("kubectl", "apply", "-f", file))
		Expect(err).NotTo(HaveOccurred(), out)

		By("blocking the database, the API server and public DNS, and keeping the gateway, DNS and the internet")
		Eventually(func() bool { return reach(db) }, time.Minute, 2*time.Second).Should(BeFalse(),
			"The policy does not block the gateway database.")
		Expect(reach(apiAddr)).To(BeFalse(), "The policy does not block the API server.")
		Expect(reach(dnsOut)).To(BeFalse(), "The policy does not block a public DNS server.")
		Expect(reach(gw)).To(BeTrue(), "The policy blocks the gateway.")
		Expect(reach(gwSvc)).To(BeTrue(), "The policy blocks the gateway Service.")
		Expect(reach(public)).To(BeTrue(), "The policy blocks the public internet.")
		out, err = utils.Run(exec.Command("kubectl", "exec", "-n", npNS, probe, "--",
			"getent", "hosts", "kubernetes.default.svc.cluster.local"))
		Expect(err).NotTo(HaveOccurred(), "The policy blocks DNS: "+out)
	})
})
