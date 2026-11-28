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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
	"github.com/mohanraj00/koodam/test/utils"
)

// zoneInstall is one install of the chart with a zone. It has its own manager, gateway, database
// and egress proxy.
type zoneInstall struct {
	zone, release, systemNS, appsNS, proxyIP string
	// gatewayPort and appPort are local ports that forward to the gateway and to the claim
	// check of the App pod.
	gatewayPort, appPort string
	// quota is the count of model calls that the tenant tenantQuota has in one period.
	quota int64
	// key is the master key of the gateway of this install. The suite never prints it.
	key      string
	forwards []*exec.Cmd
}

// fullname is the name prefix of the objects of the install.
func (z *zoneInstall) fullname() string { return z.release + "-koodam" }

// gatewayURL is the local address of the gateway of the install.
func (z *zoneInstall) gatewayURL() string { return "http://127.0.0.1:" + z.gatewayPort }

// appURL is the local address of the claim check of the App pod of the install.
func (z *zoneInstall) appURL() string { return "http://127.0.0.1:" + z.appPort + "/" }

// model is the name of the model route of the zone.
func (z *zoneInstall) model() string { return "model-" + z.zone }

// answer sends the request of build. Only a transport error gets a new try, at most 5 times. An
// HTTP answer comes back at once, so a wrong answer fails the check that reads it.
func answer(build func() *http.Request) (int, http.Header, string) {
	GinkgoHelper()
	var last error
	for range 5 {
		resp, err := httpClient.Do(build())
		if err != nil {
			last = err
			time.Sleep(2 * time.Second)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			last = err
			time.Sleep(2 * time.Second)
			continue
		}
		return resp.StatusCode, resp.Header, strings.TrimSpace(string(body))
	}
	Fail("The request got no HTTP answer in 5 tries: " + last.Error())
	return 0, nil, ""
}

// app sends one request to the App of the install, with a signed claim of the tenant. With a
// zone, the claim has the zone. It gives back the status and the typed reason of a refusal.
func (z *zoneInstall) app(tenant, zone string) (int, string) {
	GinkgoHelper()
	token := claimTokenFor("zones-e2e", tenant, zone)
	status, header, body := answer(func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, z.appURL(), nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	})
	reason := header.Get(appclaim.ReasonHeader)
	if reason != "" {
		Expect(body).To(Equal(reason), "The body of a refusal is the typed reason.")
	}
	return status, reason
}

// chat sends one model call to the gateway of the install for the tenant. It gives back the
// status and the body.
func (z *zoneInstall) chat(model, tenant string) (int, string) {
	GinkgoHelper()
	payload, err := json.Marshal(map[string]any{"model": model, "user": tenant, "max_tokens": 5,
		"messages": []map[string]string{{"role": "user", "content": "hello"}}})
	Expect(err).NotTo(HaveOccurred())
	status, _, body := answer(func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, z.gatewayURL()+"/v1/chat/completions", bytes.NewReader(payload))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+z.key)
		req.Header.Set("Content-Type", "application/json")
		return req
	})
	// The assertion is on a boolean. Thus a failure message does not have the body or the key.
	Expect(strings.Contains(body, z.key)).To(BeFalse(), "The answer of the gateway has the master key.")
	return status, body
}

// forward starts "kubectl port-forward" to a target of the install. The cleanup stops it.
func (z *zoneInstall) forward(namespace, target, ports string) {
	cmd := exec.Command("kubectl", "-n", namespace, "port-forward", target, ports)
	cmd.Stdout, cmd.Stderr = GinkgoWriter, GinkgoWriter
	Expect(cmd.Start()).To(Succeed())
	z.forwards = append(z.forwards, cmd)
}

// quotaOf reads a TenantQuota of the install.
func (z *zoneInstall) quotaOf(name string) *corev1alpha1.TenantQuota {
	GinkgoHelper()
	q := &corev1alpha1.TenantQuota{}
	Expect(k8s.Get(ctx, client.ObjectKey{Namespace: z.appsNS, Name: name}, q)).To(Succeed())
	return q
}

// The spec has 2 installs of the chart in the one kind cluster, each with its own zone. Each
// install has one App and 2 tenants with a TenantQuota. The tenant labels are the same in the 2
// installs, so the spec also shows that an install counts only its own tenants.
//
// The install of the suite reads each namespace. Thus the spec stops its manager, and starts it
// again at the end.
var _ = Describe("Tenants and zones", Ordered, Label("zones"), func() {
	const (
		tenantFast, tenantQuota = "team-fast", "team-quota"
		// tenantFree has no TenantQuota, so it has no limit.
		tenantFree         = "team-free"
		appName, version   = "ledger", "ledger-v1"
		mockModel          = "mock-1"
		suiteManager       = "deployment/" + release + "-controller-manager"
		outOfZone          = "RouteOutOfZone"
		reasonRateLimited  = corev1alpha1.ReasonTenantRateLimited
		reasonQuota        = corev1alpha1.ReasonTenantQuotaExceeded
		reasonZoneMismatch = appclaim.ReasonZoneMismatch
	)
	zoneA := &zoneInstall{zone: "zone-a", release: "zone-a", systemNS: "zone-a-system", appsNS: "zone-a-apps",
		proxyIP: "10.96.100.101", gatewayPort: "14101", appPort: "18101", quota: 2}
	zoneB := &zoneInstall{zone: "zone-b", release: "zone-b", systemNS: "zone-b-system", appsNS: "zone-b-apps",
		proxyIP: "10.96.100.102", gatewayPort: "14102", appPort: "18102", quota: 3}
	zones := []*zoneInstall{zoneA, zoneB}
	other := func(z *zoneInstall) *zoneInstall {
		if z == zoneA {
			return zoneB
		}
		return zoneA
	}
	var koodam string

	// values gives back the values file of one install. The 2 installs have the same model list:
	// one route for each zone.
	values := func(z *zoneInstall) string {
		route := func(zone string) string {
			return fmt.Sprintf("      - model_name: model-%[1]s\n        zone: %[1]s\n        litellm_params:\n"+
				"          model: openai/model-%[1]s\n          api_key: not-used\n          mock_response: in the zone\n", zone)
		}
		text := egressProxyValuesFor(z.proxyIP) + fmt.Sprintf(`zone:
  name: %[1]s
  namespaces: [%[2]s]
# The release of the suite has the core CRDs and their conversion webhook.
conversion:
  enabled: false
apps:
  enabled: true
  sharedResources: false
  claimCheck:
    zoneClaim: zone
workspace:
  networkPolicy:
    namespaces: [%[2]s]
tenants:
  usageInterval: 5s
gateway:
  bundled:
    litellm:
      modelList:
`, z.zone, z.appsNS) + route(zoneA.zone) + route(zoneB.zone)
		file := filepath.Join(GinkgoT().TempDir(), "values-"+z.zone+".yaml")
		Expect(os.WriteFile(file, []byte(text), 0o600)).To(Succeed())
		return file
	}
	appVersion := func(z *zoneInstall) *corev1alpha1.AppVersion {
		return &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: version, Namespace: z.appsNS},
			Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: appName},
				Template: corev1alpha1.AppVersionTemplate{
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: pythonImage,
						Command: []string{"python", "-u", "-c", appServer},
						Ports:   []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("http")}}},
					}}}}}}}
	}
	quotas := func(z *zoneInstall) []*corev1alpha1.TenantQuota {
		om := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: z.appsNS} }
		return []*corev1alpha1.TenantQuota{
			{ObjectMeta: om(tenantFast), Spec: corev1alpha1.TenantQuotaSpec{Tenant: tenantFast,
				RateLimit: &corev1alpha1.TenantRateLimit{RequestsPerSecond: resource.MustParse("100m"), Burst: new(int32(1))}}},
			{ObjectMeta: om(tenantQuota), Spec: corev1alpha1.TenantQuotaSpec{Tenant: tenantQuota,
				Period: corev1alpha1.TenantQuotaPeriodDay, Requests: new(z.quota)}},
		}
	}
	// refusedAtQuota waits until the claim check refuses the tenant at its quota. Before the
	// limits file gets to the pod, the answer is 200. Each other answer stops the wait.
	refusedAtQuota := func(z *zoneInstall) {
		GinkgoHelper()
		Eventually(func() (string, error) {
			status, reason := z.app(tenantQuota, z.zone)
			switch {
			case status == http.StatusOK && reason == "":
				return "", nil
			case status == http.StatusTooManyRequests && reason == reasonQuota:
				return reason, nil
			}
			return "", StopTrying(fmt.Sprintf("The App of %s gave the status %d and the reason %q.", z.zone, status, reason))
		}, 5*time.Minute, 5*time.Second).Should(Equal(reasonQuota))
	}

	BeforeAll(func() {
		koodam = filepath.Join(GinkgoT().TempDir(), "koodam")
		_, err := utils.Run(exec.Command("go", "build", "-o", koodam, "./cmd/koodam"))
		Expect(err).NotTo(HaveOccurred())

		By("stopping the manager of the suite install, because it reads each namespace")
		_, err = utils.Run(exec.Command("kubectl", "-n", systemNS, "scale", suiteManager, "--replicas=0"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			By("starting the manager of the suite install again")
			_, err := utils.Run(exec.Command("kubectl", "-n", systemNS, "scale", suiteManager, "--replicas=1"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", systemNS, "rollout", "status", suiteManager, "--timeout", "5m"))
			Expect(err).NotTo(HaveOccurred())
		})
		_, err = utils.Run(exec.Command("kubectl", "-n", systemNS, "wait", "--for=delete", "pod",
			"-l", "control-plane=controller-manager", "--timeout", "2m"))
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			for _, z := range zones {
				for _, f := range z.forwards {
					if f.Process != nil {
						_ = f.Process.Kill()
					}
				}
				// The manager of the install must remove the finalizers before the uninstall.
				_ = k8s.DeleteAllOf(ctx, &corev1alpha1.AppVersion{}, client.InNamespace(z.appsNS))
				_ = k8s.DeleteAllOf(ctx, &corev1alpha1.TenantQuota{}, client.InNamespace(z.appsNS))
				Eventually(func() int {
					versions, list := &corev1alpha1.AppVersionList{}, &corev1alpha1.TenantQuotaList{}
					_ = k8s.List(ctx, versions, client.InNamespace(z.appsNS))
					_ = k8s.List(ctx, list, client.InNamespace(z.appsNS))
					return len(versions.Items) + len(list.Items)
				}, 3*time.Minute, 2*time.Second).Should(BeZero(), "A finalizer kept an object of "+z.zone+".")
				_, _ = utils.Run(exec.Command("helm", "uninstall", z.release, "-n", z.systemNS, "--wait"))
				for _, ns := range []string{z.appsNS, z.systemNS} {
					_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
				}
			}
		})

		By("making the namespaces and the master key of each install")
		for _, z := range zones {
			for _, ns := range []string{z.systemNS, z.appsNS} {
				Expect(client.IgnoreAlreadyExists(k8s.Create(ctx,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))).To(Succeed())
			}
			b := make([]byte, 24)
			_, err := rand.Read(b)
			Expect(err).NotTo(HaveOccurred())
			z.key = "sk-" + hex.EncodeToString(b)
			Expect(k8s.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "koodam-gateway", Namespace: z.systemNS},
				StringData: map[string]string{"master-key": z.key}})).To(Succeed())
		}

		By("installing the 2 releases of the chart at the same time")
		dir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		i := strings.LastIndex(managerImage, ":")
		type result struct {
			zone, out string
			err       error
		}
		done := make(chan result, len(zones))
		for _, z := range zones {
			cmd := exec.Command("helm", "upgrade", "--install", z.release, "charts/koodam", "-n", z.systemNS,
				"-f", "charts/koodam/values-bundled.yaml", "-f", claimValues(), "-f", values(z),
				"--set", "image.repository="+managerImage[:i], "--set", "image.tag="+managerImage[i+1:],
				"--wait", "--timeout", "10m")
			cmd.Dir = dir
			go func() {
				out, err := cmd.CombinedOutput()
				done <- result{zone: z.zone, out: string(out), err: err}
			}()
		}
		for range zones {
			r := <-done
			Expect(r.err).NotTo(HaveOccurred(), "Failed to install the chart of "+r.zone+": "+r.out)
		}

		By("forwarding a local port to the gateway of each install")
		for _, z := range zones {
			z.forward(z.systemNS, "svc/"+z.fullname()+"-litellm", z.gatewayPort+":4000")
			Eventually(func() int {
				resp, err := httpClient.Get(z.gatewayURL() + "/health/readiness")
				if err != nil {
					return 0
				}
				_ = resp.Body.Close()
				return resp.StatusCode
			}, 3*time.Minute, time.Second).Should(Equal(http.StatusOK))
		}

		By("making 2 tenants with a TenantQuota in each install")
		for _, z := range zones {
			for _, q := range quotas(z) {
				Expect(k8s.Create(ctx, q)).To(Succeed())
			}
		}
		for _, z := range zones {
			for _, name := range []string{tenantFast, tenantQuota} {
				Eventually(func() bool {
					return meta.IsStatusConditionTrue(z.quotaOf(name).Status.Conditions, corev1alpha1.TenantQuotaReady)
				}, 3*time.Minute, 2*time.Second).Should(BeTrue(), "The TenantQuota "+name+" of "+z.zone+" is not Ready.")
			}
		}

		By("making one App with one version in each install")
		for _, z := range zones {
			Expect(k8s.Create(ctx, &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: z.appsNS}})).
				To(Succeed())
			Expect(k8s.Create(ctx, appVersion(z))).To(Succeed())
		}
		for _, z := range zones {
			Eventually(func() error {
				_, err := utils.Run(exec.Command("kubectl", "-n", z.appsNS, "rollout", "status",
					"deployment/kd-"+version, "--timeout", "10s"))
				return err
			}, 5*time.Minute, 2*time.Second).Should(Succeed(), "The pod of the App of "+z.zone+" did not start.")
			// 15080 is the first port of the check container.
			z.forward(z.appsNS, "deployment/kd-"+version, z.appPort+":15080")
			// The first answer shows that the forward and the claim check are in operation.
			Eventually(func() int {
				req, err := http.NewRequest(http.MethodGet, z.appURL(), nil)
				if err != nil {
					return 0
				}
				req.Header.Set("Authorization", "Bearer "+claimTokenFor("zones-e2e", tenantFree, z.zone))
				resp, err := httpClient.Do(req)
				if err != nil {
					return 0
				}
				_ = resp.Body.Close()
				return resp.StatusCode
			}, 2*time.Minute, time.Second).Should(Equal(http.StatusOK))
		}
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, z := range zones {
			for _, args := range [][]string{
				{"get", "tenantquotas,apps,appversions,deployments,pods,configmaps", "-n", z.appsNS, "-o", "wide"},
				{"get", "tenantquotas", "-n", z.appsNS, "-o", "yaml"},
				{"get", "pods", "-n", z.systemNS, "-o", "wide"},
				{"logs", "-n", z.systemNS, "-l", "control-plane=controller-manager", "--tail", "100"},
				{"logs", "-n", z.appsNS, "deployment/kd-" + version, "-c", "koodam-claim", "--tail", "50"},
			} {
				out, _ := utils.Run(exec.Command("kubectl", args...))
				_, _ = fmt.Fprintln(GinkgoWriter, out)
			}
		}
	})

	It("limits the rate of one tenant and not of the other tenant", func() {
		for _, z := range zones {
			By("sending 2 requests of the tenant with the rate limit to the App of " + z.zone)
			status, reason := z.app(tenantFast, z.zone)
			Expect([]any{status, reason}).To(Equal([]any{http.StatusOK, ""}), "The first request uses the burst.")
			status, reason = z.app(tenantFast, z.zone)
			Expect([]any{status, reason}).To(Equal([]any{http.StatusTooManyRequests, reasonRateLimited}))

			By("sending 5 requests of the other tenant")
			for range 5 {
				status, reason := z.app(tenantQuota, z.zone)
				Expect([]any{status, reason}).To(Equal([]any{http.StatusOK, ""}), "The rate limit is for one tenant only.")
			}
		}
	})

	It("refuses a tenant at its period quota, in its own install only", func() {
		for _, z := range zones {
			By(fmt.Sprintf("sending %d model calls of the tenant to the gateway of %s", z.quota, z.zone))
			for range z.quota {
				status, body := z.chat(mockModel, tenantQuota)
				Expect(status).To(Equal(http.StatusOK), body)
			}
			By("waiting for the mark of the quota in the TenantQuota")
			Eventually(func() []any {
				q := z.quotaOf(tenantQuota)
				return []any{q.Status.Requests, meta.IsStatusConditionTrue(q.Status.Conditions, corev1alpha1.TenantQuotaExceeded)}
			}, 3*time.Minute, 2*time.Second).Should(Equal([]any{z.quota, true}))
			By("waiting for the refusal of the claim check")
			refusedAtQuota(z)

			By("sending a request of the other tenant, which has no quota")
			Eventually(func() (int, error) {
				status, reason := z.app(tenantFast, z.zone)
				if status == http.StatusOK || reason == reasonRateLimited {
					return status, nil
				}
				return 0, StopTrying(fmt.Sprintf("The status is %d and the reason is %q.", status, reason))
			}, time.Minute, 3*time.Second).Should(Equal(http.StatusOK))

			if z == zoneA {
				By("making sure that the same tenant label of zone-b is not at its quota")
				far := other(z)
				status, reason := far.app(tenantQuota, far.zone)
				Expect([]any{status, reason}).To(Equal([]any{http.StatusOK, ""}))
				q := far.quotaOf(tenantQuota)
				Expect(q.Status.Requests).To(BeZero(), "The gateway of zone-b counted a call of zone-a.")
				Expect(meta.IsStatusConditionTrue(q.Status.Conditions, corev1alpha1.TenantQuotaExceeded)).To(BeFalse())
			}
		}
	})

	It("refuses a claim of a different zone with ZoneMismatch", func() {
		for _, z := range zones {
			By("sending a claim of " + other(z).zone + " to the App of " + z.zone)
			status, reason := z.app(tenantFree, other(z).zone)
			Expect([]any{status, reason}).To(Equal([]any{http.StatusForbidden, reasonZoneMismatch}))
			By("sending a claim of the zone, and a claim with no zone")
			for _, zone := range []string{z.zone, ""} {
				status, reason := z.app(tenantFree, zone)
				Expect([]any{status, reason}).To(Equal([]any{http.StatusOK, ""}))
			}
			By("reading the span of the refusal")
			out, err := exec.Command("kubectl", "-n", z.appsNS, "logs", "deployment/kd-"+version, "-c", "koodam-claim",
				"--tail=-1").Output()
			Expect(err).NotTo(HaveOccurred())
			found := false
			for line := range strings.Lines(string(out)) {
				var sp appclaim.Span
				if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &sp) != nil {
					continue
				}
				Expect(sp.Zone).To(Equal(z.zone), "Each span has the zone of the install: %s", line)
				if sp.Outcome == reasonZoneMismatch {
					Expect(sp.Status).To(Equal(http.StatusForbidden))
					Expect(sp.Tenant).To(Equal(tenantFree))
					found = true
				}
			}
			Expect(found).To(BeTrue(), "The check container of "+z.zone+" has no span of the refusal.")
		}
	})

	It("refuses a model route of a different zone with RouteOutOfZone", func() {
		for _, z := range zones {
			By("calling the route of " + other(z).zone + " in the gateway of " + z.zone)
			status, body := z.chat(other(z).model(), tenantFree)
			Expect(status).To(Equal(http.StatusForbidden), body)
			Expect(body).To(ContainSubstring(outOfZone + ": "))
			By("calling the route of the zone")
			status, body = z.chat(z.model(), tenantFree)
			Expect(status).To(Equal(http.StatusOK), body)
			By("reading the log line of the refusal")
			out, err := exec.Command("kubectl", "-n", z.systemNS, "logs", "deployment/"+z.fullname()+"-litellm",
				"--tail=-1").Output()
			Expect(err).NotTo(HaveOccurred())
			// The key check is first and on a boolean. Thus no failure message has a log with the key.
			Expect(strings.Contains(string(out), z.key)).To(BeFalse(), "The log of the gateway has the master key.")
			Expect(string(out)).To(ContainSubstring(fmt.Sprintf(
				`{"kind": "gateway-zone", "outcome": %q, "zone": %q, "model": %q}`, outOfZone, z.zone, other(z).model())))
		}
	})

	It("shows only the tenants of the install in its spend report", func() {
		for _, z := range zones {
			out, err := exec.Command(koodam, "usage", "--by", "tenant", "-n", z.appsNS).Output()
			Expect(err).NotTo(HaveOccurred())
			_, _ = fmt.Fprintln(GinkgoWriter, string(out))
			requests := map[string]string{}
			for line := range strings.Lines(string(out)) {
				if f := strings.Fields(line); len(f) >= 3 && strings.HasPrefix(f[0], "team-") {
					requests[f[0]] = f[2]
				}
			}
			// The quota of the tenant is 2 in zone-a and 3 in zone-b. Each gateway counted only the
			// calls of its own install, and a refused model route is not a call of a tenant.
			Expect(requests).To(Equal(map[string]string{tenantFast: "0", tenantQuota: fmt.Sprint(z.quota)}),
				"The report of "+z.zone+" is: "+string(out))
		}
	})
})
