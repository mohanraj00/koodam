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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/test/utils"
)

const (
	// agentLabel is on each object that the controller makes for an Agent.
	agentLabel = "koodam.dev/agent"
	// curlImage runs the test agents. It has sh and curl.
	curlImage = "docker.io/curlimages/curl:8.11.1"
	// mockModel is the bundled mock model. Its calls have a spend that is more than zero.
	mockModel = "mock-1"
	// residualWait is the time after the end of a run when the test probes the credentials.
	residualWait = 30 * time.Second
	// controllerSelector selects the controller pod of the release.
	controllerSelector = "control-plane=controller-manager,app.kubernetes.io/instance=" + release
)

// call is a shell command that calls the model with the run key and prints the HTTP status.
// The command uses the environment variables. It does not write the key value.
const call = `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $OPENAI_API_KEY" ` +
	`-H 'Content-Type: application/json' "$OPENAI_BASE_URL/chat/completions" ` +
	`-d "{\"model\":\"$ANTHROPIC_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"`

// callsScript makes n model calls and then waits for sleep seconds. A TERM signal ends it.
func callsScript(n, sleep int) string {
	return "trap 'exit 0' TERM\nfor i in $(seq " + strconv.Itoa(n) + `); do [ "$(` + call +
		`)" = 200 ] || exit 1; sleep 0.2; done` + "\nsleep " + strconv.Itoa(sleep) + " & wait"
}

// budgetScript calls the model until the gateway refuses a call. It fails after 500 calls.
const budgetScript = `for i in $(seq 500); do
c=$(` + call + `)
case "$c" in 200) sleep 0.2 ;; 4*) exit 0 ;; *) exit 1 ;; esac
done
exit 1`

// run has the credentials of a live run. The test never prints them.
type run struct {
	key   string
	token string
}

var _ = Describe("Residual access", Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"-n", systemNS, "logs", "-l", controllerSelector, "--tail", "200"},
			{"-n", runNS, "get", "agents", "-o", "yaml"},
			{"-n", runNS, "get", "events", "--sort-by=.lastTimestamp"},
		} {
			out, _ := utils.Run(exec.Command("kubectl", args...))
			_, _ = fmt.Fprintln(GinkgoWriter, out)
		}
	})

	It("runs the sample Agent to Succeeded and leaves no access", func() {
		a := sampleAgent("normal", callsScript(3, 15))
		r := start(a)

		By("checking that the run credentials work while the run is live")
		Expect(modelCall(r.key)).To(Equal(http.StatusOK), "The run key did not work during the run.")
		allowed, answered := canListPods(r.token)
		Expect(answered && allowed).To(BeTrue(), "The run token could not list pods during the run.")

		end := waitEnd(a)
		expectNoAccess(a, r, end, false)
		Expect(a.Status.Usage).NotTo(BeNil())
		Expect(a.Status.Usage.Calls).To(BeNumerically(">=", 3))
	})

	It("leaves no access after an Agent delete during the run", func() {
		a := sampleAgent("deleted", callsScript(1, 600))
		r := start(a)

		By("deleting the Agent during the run")
		end := time.Now()
		Expect(k8s.Delete(ctx, a)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(a), a))
		}).Should(BeTrue(), "The Agent was not deleted.")
		expectNoAccess(a, r, end, true)
	})

	It("leaves no access after a controller restart during the run", func() {
		a := sampleAgent("restart", callsScript(2, 30))
		r := start(a)

		By("deleting the controller pod during the run")
		Expect(k8s.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(systemNS),
			client.MatchingLabels{"control-plane": "controller-manager",
				"app.kubernetes.io/instance": release})).To(Succeed())

		end := waitEnd(a)
		expectNoAccess(a, r, end, false)
	})

	It("stops a budget run with an overspend of not more than the largest call", func() {
		a := sampleAgent("budget", budgetScript)
		a.Spec.Budget = resource.MustParse("0.0001")
		r := start(a)

		end := waitEnd(a)
		expectNoAccess(a, r, end, false)

		By("comparing the spend with the budget and the largest call")
		Expect(a.Status.Usage).NotTo(BeNil())
		spend, err := strconv.ParseFloat(a.Status.Usage.Spend, 64)
		Expect(err).NotTo(HaveOccurred())
		budget := a.Spec.Budget.AsApproximateFloat64()
		largest := largestCall(a.Status.KeyHash)
		_, _ = fmt.Fprintf(GinkgoWriter, "spend %g, budget %g, largest call %g, calls %d\n",
			spend, budget, largest, a.Status.Usage.Calls)
		Expect(spend).To(BeNumerically(">=", budget), "The gateway refused a call before the budget.")
		Expect(spend-budget).To(BeNumerically("<=", largest), "The overspend is more than the largest call.")
	})
})

// sampleAgent reads config/samples/core_v1alpha1_agent.yaml and changes it to run script with the
// mock model in runNS. The budget, the deadline and the rules stay as in the sample.
func sampleAgent(name, script string) *corev1alpha1.Agent {
	dir, err := utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())
	f, err := os.Open(filepath.Join(dir, "config", "samples", "core_v1alpha1_agent.yaml"))
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = f.Close() }()
	a := &corev1alpha1.Agent{}
	Expect(yaml.NewYAMLOrJSONDecoder(f, 4096).Decode(a)).To(Succeed())
	a.Name, a.Namespace = name, runNS
	a.Spec.Image, a.Spec.Model = curlImage, mockModel
	a.Spec.Command = []string{"sh", "-c", script}
	return a
}

// start makes the Agent, waits until the run is live, and gets the key and a ServiceAccount token.
func start(a *corev1alpha1.Agent) run {
	By("making the Agent " + a.Name)
	Expect(k8s.Create(ctx, a)).To(Succeed())
	Eventually(func() corev1alpha1.AgentPhase {
		_ = k8s.Get(ctx, client.ObjectKeyFromObject(a), a)
		return a.Status.Phase
	}).Should(Equal(corev1alpha1.AgentRunning))

	By("reading the run key and making a run token while the run is live")
	s := &corev1.Secret{}
	Expect(k8s.Get(ctx, client.ObjectKey{Namespace: runNS, Name: a.Status.Job}, s)).To(Succeed())
	key := string(s.Data["OPENAI_API_KEY"])
	Expect(key != "").To(BeTrue(), "The run Secret has no key.")
	tr, err := clientset.CoreV1().ServiceAccounts(runNS).CreateToken(ctx, a.Status.Job,
		&authnv1.TokenRequest{}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
	return run{key: key, token: tr.Status.Token}
}

// waitEnd waits until the Agent is Succeeded and returns the time when its Job completed.
func waitEnd(a *corev1alpha1.Agent) time.Time {
	By("waiting for the end of the run")
	Eventually(func() corev1alpha1.AgentPhase {
		_ = k8s.Get(ctx, client.ObjectKeyFromObject(a), a)
		return a.Status.Phase
	}).Should(Or(Equal(corev1alpha1.AgentSucceeded), Equal(corev1alpha1.AgentFailed)))
	Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentSucceeded))
	job := &batchv1.Job{}
	Expect(k8s.Get(ctx, client.ObjectKey{Namespace: runNS, Name: a.Status.Job}, job)).To(Succeed())
	Expect(job.Status.CompletionTime).NotTo(BeNil())
	return job.Status.CompletionTime.Time
}

// expectNoAccess probes the run credentials 30 s after end. If gone is true, the Agent is deleted.
func expectNoAccess(a *corev1alpha1.Agent, r run, end time.Time, gone bool) {
	By("waiting until 30 s after the end of the run")
	time.Sleep(time.Until(end.Add(residualWait)))

	By("calling the model with the run key")
	code := modelCall(r.key)
	Expect(code >= 400 && code < 500).To(BeTrue(), "The gateway did not refuse the run key after the run.")

	By("calling the API with the run token")
	_, err := tokenClient(r.token).CoreV1().Pods(runNS).List(ctx, metav1.ListOptions{})
	Expect(apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err)).To(BeTrue(),
		"The API server did not refuse the run token after the run.")

	By("asking the API server if the run token can list pods")
	allowed, answered := canListPods(r.token)
	Expect(answered).To(BeTrue(), "The access review with the run token did not get an answer.")
	Expect(allowed).To(BeFalse(), "The run token can list pods after the run.")

	By("listing the objects with the label " + agentLabel)
	lists := []client.ObjectList{&corev1.SecretList{}, &corev1.ServiceAccountList{},
		&rbacv1.RoleList{}, &rbacv1.RoleBindingList{}}
	if gone {
		// The controller keeps the Job of a run that ended, until the Agent is deleted.
		lists = append(lists, &batchv1.JobList{})
	}
	for _, l := range lists {
		Expect(k8s.List(ctx, l, client.InNamespace(runNS), client.MatchingLabels{agentLabel: a.Name})).To(Succeed())
		Expect(meta.LenList(l)).To(BeZero(), fmt.Sprintf("A %T item of the run exists after the run.", l))
	}
	if !gone {
		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(a.Status.Conditions, "Revoked")).To(BeTrue())
	}
}

// modelCall calls the mock model through the gateway with key and returns the HTTP status.
// It does not print the key or the response body.
func modelCall(key string) int {
	body := `{"model":"` + mockModel + `","messages":[{"role":"user","content":"probe"}]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/chat/completions",
		strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "The gateway did not answer.")
	_ = resp.Body.Close()
	return resp.StatusCode
}

// tokenClient is a client that uses only token. It has no client certificate.
func tokenClient(token string) *kubernetes.Clientset {
	cfg := rest.AnonymousClientConfig(restCfg)
	cfg.BearerToken = token
	cs, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	return cs
}

// canListPods sends a SelfSubjectAccessReview with token, as "kubectl auth can-i list pods" does.
// answered is false if the API server did not answer or did not refuse the token.
func canListPods(token string) (allowed, answered bool) {
	ssar := &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authzv1.ResourceAttributes{Namespace: runNS, Verb: "list", Resource: "pods"},
	}}
	res, err := tokenClient(token).AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar,
		metav1.CreateOptions{})
	if err != nil {
		return false, apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err)
	}
	return res.Status.Allowed, true
}

// largestCall returns the largest spend of one call with the key, from the gateway spend logs.
func largestCall(keyHash string) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/spend/logs?api_key="+keyHash, nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+masterKey)
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "The gateway did not answer.")
	defer func() { _ = resp.Body.Close() }()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	var logs []struct {
		Spend float64 `json:"spend"`
	}
	Expect(json.NewDecoder(resp.Body).Decode(&logs)).To(Succeed())
	largest := 0.0
	for _, l := range logs {
		largest = max(largest, l.Spend)
	}
	Expect(largest).To(BeNumerically(">", 0), "The spend logs have no call with a spend.")
	return largest
}
