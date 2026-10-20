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
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// harnessImage runs the harness Agents. It has glibc for the Claude Code build.
	harnessImage = "docker.io/library/debian:bookworm-slim"
	// codexModel sends the calls to the fake upstream. See testdata/values-e2e.yaml.
	codexModel = "fake-responses"
	// residualWait is the time after the end of a run when the test probes the credentials.
	residualWait = 30 * time.Second
	// probeWindow is the time after residualWait in which all the probes must complete.
	probeWindow = 5 * time.Second
	// controllerSelector selects the controller pod of the release.
	controllerSelector = "control-plane=controller-manager,app.kubernetes.io/instance=" + release
	// metricsName is the name of the test ServiceAccount, ClusterRole and ClusterRoleBinding
	// for the metrics scrape.
	metricsName = "koodam-e2e-metrics"
	metricsPort = "18443"
	// jobTTL is the chart value jobTTLSeconds for the test. The controller deletes a run Job this time
	// after the end. The test does not wait for the TTL, so the Jobs stay for the log checks.
	jobTTL = 3600
)

// call is a shell command that calls the model with the run key and prints the HTTP status.
// The command uses the environment variables. It does not write the key value.
const call = `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $OPENAI_API_KEY" ` +
	`-H 'Content-Type: application/json' "$OPENAI_BASE_URL/chat/completions" ` +
	`-d "{\"model\":\"$ANTHROPIC_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"`

// pause is the time between 2 calls of a run. It is longer than the 1 s spend write interval of
// the gateway, so the gateway knows the spend of a call before the next call.
const pause = "1.5"

// callsScript makes n model calls and then waits for sleep seconds. A TERM signal ends it.
func callsScript(n, sleep int) string {
	return "trap 'exit 0' TERM\nfor i in $(seq " + strconv.Itoa(n) + `); do [ "$(` + call +
		`)" = 200 ] || exit 1; sleep ` + pause + "; done\nsleep " + strconv.Itoa(sleep) + " & wait"
}

// budgetScript calls the model until the gateway refuses a call. It fails after 500 calls.
const budgetScript = `for i in $(seq 500); do
c=$(` + call + `)
case "$c" in 200) sleep ` + pause + ` ;; 4*) exit 0 ;; *) exit 1 ;; esac
done
exit 1`

// codexScript runs Codex with a model provider for the gateway. Codex does not read
// OPENAI_BASE_URL, and it uses only the Responses API.
const codexScript = `cd /tmp && codex exec --skip-git-repo-check -m "$ANTHROPIC_MODEL" -c model_provider=koodam ` +
	`-c "model_providers.koodam={name=\"koodam\",base_url=\"$OPENAI_BASE_URL\",env_key=\"OPENAI_API_KEY\",` +
	`wire_api=\"responses\"}" 'Say hi.' </dev/null`

// run has the credentials of a live run. The test never prints them.
type run struct {
	key   string
	token string
}

var _ = Describe("Residual access", Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	AfterEach(dumpOnFailure)

	It("runs the sample Agent to Succeeded and leaves no access", func() {
		a := sampleAgent("normal", callsScript(3, 15))
		r := start(a)

		By("checking that the run Job has no TTL, because the controller deletes it")
		job := &batchv1.Job{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: runNS, Name: a.Status.Job}, job)).To(Succeed())
		Expect(job.Spec.TTLSecondsAfterFinished).To(BeNil(), "The run Job has a TTL.")

		By("checking that the run credentials work while the run is live")
		code, _ := modelCall(ctx, r.key)
		Expect(code).To(Equal(http.StatusOK), "The run key did not work during the run.")
		allowed, err := canListPods(ctx, r.token)
		Expect(err == nil && allowed).To(BeTrue(), "The run token could not list pods during the run.")

		expectNoAccess(a, r, jobEnd(a))
		expectSucceeded(a)
		Expect(a.Status.Usage.Calls).To(BeNumerically(">=", 3))
	})

	It("leaves no access after an Agent delete during the run", func() {
		a := sampleAgent("deleted", callsScript(1, 600))
		r := start(a)

		By("deleting the Agent during the run")
		end := time.Now()
		Expect(k8s.Delete(ctx, a)).To(Succeed())
		expectNoAccess(a, r, end)
		Eventually(func() bool {
			return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(a), a))
		}, time.Minute).Should(BeTrue(), "The Agent was not deleted.")
	})

	It("leaves no access after a controller restart during the run", func() {
		a := sampleAgent("restart", callsScript(2, 30))
		r := start(a)

		By("deleting the controller pod during the run")
		Expect(k8s.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(systemNS),
			client.MatchingLabels{"control-plane": "controller-manager",
				"app.kubernetes.io/instance": release})).To(Succeed())

		expectNoAccess(a, r, jobEnd(a))
		expectSucceeded(a)
	})

	It("stops a budget run with an overspend of not more than the largest call", func() {
		a := sampleAgent("budget", budgetScript)
		a.Spec.Budget = resource.MustParse("0.0001")
		r := start(a)

		expectNoAccess(a, r, jobEnd(a))
		expectSucceeded(a)

		By("comparing the spend with the budget and the largest call")
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

// The harness runs check the residual access, the harness in the status and the key, and the
// usage of each harness.
var _ = Describe("Harness runs", Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	AfterEach(dumpOnFailure)

	It("runs Claude Code through the gateway", func() {
		a := harnessAgent("claude-run", "claude-code", mockModel, `claude -p 'Say hi.' --model "$ANTHROPIC_MODEL"`)
		a.Spec.Env = map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
		expectHarnessRun(a)
	})

	// This is the only Codex run of the suite. The installer reads the GitHub API with no token,
	// and that API has a low rate limit (#36).
	It("runs Codex through the gateway", func() {
		a := harnessAgent("codex-run", "codex", codexModel, codexScript)
		expectHarnessRun(a)
	})
})

var _ = Describe("Metrics", func() {
	It("serves the metrics to an authorized scrape only", func() {
		By("making a ServiceAccount that can read the metrics")
		objs := []client.Object{
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: metricsName, Namespace: systemNS}},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: metricsName}, Rules: []rbacv1.PolicyRule{
				{NonResourceURLs: []string{"/metrics"}, Verbs: []string{"get"}}}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: metricsName},
				RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: metricsName},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: metricsName, Namespace: systemNS}}},
		}
		for _, o := range objs {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}
		tr, err := clientset.CoreV1().ServiceAccounts(systemNS).CreateToken(ctx, metricsName,
			&authnv1.TokenRequest{}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		pf := forward(release+"-metrics-service", metricsPort+":8443")
		defer func() { _ = pf.Process.Kill() }()
		scrape := func(token string) (int, string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+metricsPort+"/metrics", nil)
			Expect(err).NotTo(HaveOccurred())
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			// The metrics endpoint has a self-signed certificate.
			c := &http.Client{Timeout: callTimeout, Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
			resp, err := c.Do(req)
			if err != nil {
				return 0, ""
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		By("scraping the metrics with the token")
		Eventually(func(g Gomega) {
			code, body := scrape(tr.Status.Token)
			g.Expect(code).To(Equal(http.StatusOK))
			g.Expect(strings.Contains(body, "controller_runtime_reconcile_total")).To(BeTrue())
		}, 2*time.Minute, time.Second).Should(Succeed())
		By("scraping the metrics with no token")
		code, _ := scrape("")
		Expect(code).To(Equal(http.StatusUnauthorized))
	})
})

// dumpOnFailure writes the controller log, the Agents, the events and the run logs if the spec
// failed.
func dumpOnFailure() {
	if !CurrentSpecReport().Failed() {
		return
	}
	for _, args := range [][]string{
		{"-n", systemNS, "logs", "-l", controllerSelector, "--tail", "200"},
		{"-n", runNS, "get", "agents", "-o", "yaml"},
		{"-n", runNS, "get", "events", "--sort-by=.lastTimestamp"},
		{"-n", runNS, "logs", "-l", "batch.kubernetes.io/job-name", "--all-containers", "--prefix", "--tail", "50"},
	} {
		out, _ := utils.Run(exec.Command("kubectl", args...))
		_, _ = fmt.Fprintln(GinkgoWriter, out)
	}
}

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

// harnessAgent is a sample Agent that installs the latest version of harness and runs script.
func harnessAgent(name, harness, model, script string) *corev1alpha1.Agent {
	a := sampleAgent(name, script)
	a.Spec.Image, a.Spec.Model = harnessImage, model
	a.Spec.Harness = &corev1alpha1.HarnessSpec{Name: harness}
	return a
}

// expectHarnessRun runs the harness Agent a to Succeeded. It checks the residual access, the
// installer log, the harness in "kubectl get" and in the key metadata, and the usage.
func expectHarnessRun(a *corev1alpha1.Agent) {
	h := a.Spec.Harness.Name
	r := start(a)
	expectNoAccess(a, r, jobEnd(a))
	expectSucceeded(a)

	By("checking that the installer checked and installed a version")
	out, err := utils.Run(exec.Command("kubectl", "-n", runNS, "logs", "job/"+a.Status.Job, "-c", "harness"))
	Expect(err).NotTo(HaveOccurred())
	Expect(out).To(MatchRegexp(`harness-install: ` + h + ` [0-9]+\.[0-9]+\.[0-9]+ is in /koodam/bin`))

	By("checking that the HARNESS column of kubectl get shows the harness")
	out, err = utils.Run(exec.Command("kubectl", "-n", runNS, "get", "agent", a.Name))
	Expect(err).NotTo(HaveOccurred())
	lines := strings.Split(strings.TrimSpace(out), "\n")
	Expect(lines).To(HaveLen(2), "kubectl get did not show 1 Agent.")
	// NAME, PHASE and MODEL come before HARNESS and are never empty, so the fields stay in line.
	head, row := strings.Fields(lines[0]), strings.Fields(lines[1])
	col := slices.Index(head, "HARNESS")
	Expect(col).To(BeNumerically(">", 0), "kubectl get has no HARNESS column.")
	Expect(len(row)).To(BeNumerically(">", col))
	Expect(row[col]).To(Equal(h))

	By("checking that the blocked key has the harness in its metadata")
	info := keyInfo(a.Status.KeyHash)
	Expect(info.Metadata["harness"]).To(Equal(h))
	Expect(info.Blocked).To(BeTrue(), "The gateway did not block the key "+a.Status.KeyAlias+".")

	// The usage is from the spend logs of the run key. A call to the model that did not go
	// through the gateway with this key is not in it. Thus calls > 0 proves the gateway path.
	By("checking the usage of the run key")
	u := a.Status.Usage
	_, _ = fmt.Fprintf(GinkgoWriter, "harness %s, key %s: calls %d, failed calls %d, tokens %d/%d, spend %s\n",
		h, a.Status.KeyAlias, u.Calls, u.FailedCalls, u.InputTokens, u.OutputTokens, u.Spend)
	Expect(u.Calls).To(BeNumerically(">", 0), "The gateway has no call of the "+h+" run.")
	Expect(u.FailedCalls).To(BeZero(), "The gateway has failed calls of the "+h+" run.")
	Expect(u.InputTokens).To(BeNumerically(">", 0), "The gateway has no input tokens of the "+h+" run.")
	Expect(u.OutputTokens).To(BeNumerically(">", 0), "The gateway has no output tokens of the "+h+" run.")
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

// jobEnded is true if the Job has a true Complete or Failed condition. It returns the condition time.
func jobEnded(job *batchv1.Job) (time.Time, bool) {
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}

// jobEnd waits for the end of the run Job and returns the time of the end condition. It does not
// wait for the controller.
func jobEnd(a *corev1alpha1.Agent) time.Time {
	By("waiting for the end of the run Job")
	var end time.Time
	Eventually(func() bool {
		job := &batchv1.Job{}
		if k8s.Get(ctx, client.ObjectKey{Namespace: runNS, Name: a.Status.Job}, job) != nil {
			return false
		}
		t, ok := jobEnded(job)
		end = t
		return ok
	}).Should(BeTrue(), "The run Job did not end.")
	return end
}

// expectSucceeded checks that the Agent is Succeeded, with the Revoked condition and the usage.
// The controller writes the usage and the Revoked condition in the same status update, so the
// test waits for the condition and then reads the usage.
func expectSucceeded(a *corev1alpha1.Agent) {
	Eventually(func() bool {
		_ = k8s.Get(ctx, client.ObjectKeyFromObject(a), a)
		return meta.IsStatusConditionTrue(a.Status.Conditions, "Revoked")
	}, time.Minute).Should(BeTrue(), "The Agent has no true Revoked condition.")
	Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentSucceeded))
	Expect(meta.IsStatusConditionTrue(a.Status.Conditions, "Revoked")).To(BeTrue())
	Expect(a.Status.Usage).NotTo(BeNil())
}

// expectNoAccess probes the run credentials 30 s after end. All the probes share a deadline
// 5 s after that time. A probe that does not complete before the deadline fails the test.
func expectNoAccess(a *corev1alpha1.Agent, r run, end time.Time) {
	By("calling the model with the master key, as a control")
	code, _ := modelCall(ctx, masterKey)
	Expect(code).To(Equal(http.StatusOK), "The control call with the master key did not work.")

	By("waiting until 30 s after the end of the run")
	wait := time.Until(end.Add(residualWait))
	Expect(wait > 0).To(BeTrue(), "The test saw the end of the run more than 30 s late.")
	time.Sleep(wait)
	deadline := end.Add(residualWait + probeWindow)
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	By("calling the model with the run key")
	code, errType := modelCall(pctx, r.key)
	Expect(code == http.StatusUnauthorized && errType == "auth_error").To(BeTrue(), fmt.Sprintf(
		"The gateway did not refuse the run key as a key that is not valid: status %d, error type %q.",
		code, errType))

	By("calling the API with the run token")
	_, err := tokenClient(r.token).CoreV1().Pods(runNS).List(pctx, metav1.ListOptions{})
	Expect(apierrors.IsUnauthorized(err)).To(BeTrue(), "The API server did not refuse the run token.")

	By("asking the API server if the run token can list pods")
	allowed, err := canListPods(pctx, r.token)
	Expect(apierrors.IsUnauthorized(err) && !allowed).To(BeTrue(),
		"The access review with the run token did not get a 401.")

	By("listing the objects with the label " + agentLabel)
	sel := []client.ListOption{client.InNamespace(runNS), client.MatchingLabels{agentLabel: a.Name}}
	for _, l := range []client.ObjectList{&corev1.SecretList{}, &corev1.ServiceAccountList{},
		&rbacv1.RoleList{}, &rbacv1.RoleBindingList{}} {
		Expect(k8s.List(pctx, l, sel...)).To(Succeed())
		Expect(meta.LenList(l)).To(BeZero(), fmt.Sprintf("A %T item of the run exists after the run.", l))
	}
	// The Job of a run that ended, and its pods, stay until the Job TTL or the Agent delete.
	// They must be stopped and must hold no credential.
	jobs := &batchv1.JobList{}
	Expect(k8s.List(pctx, jobs, sel...)).To(Succeed())
	for _, j := range jobs.Items {
		_, ended := jobEnded(&j)
		Expect(ended && j.Status.Active == 0).To(BeTrue(), "A Job of the run did not stop.")
	}
	pods := &corev1.PodList{}
	Expect(k8s.List(pctx, pods, sel...)).To(Succeed())
	for _, p := range pods.Items {
		Expect(p.Status.Phase).To(BeElementOf(corev1.PodSucceeded, corev1.PodFailed), "A pod of the run did not stop.")
		for _, c := range p.Spec.Containers {
			for _, e := range c.EnvFrom {
				if e.SecretRef != nil {
					err := k8s.Get(pctx, client.ObjectKey{Namespace: runNS, Name: e.SecretRef.Name}, &corev1.Secret{})
					Expect(apierrors.IsNotFound(err)).To(BeTrue(), "A Secret of a run pod exists after the run.")
				}
			}
		}
	}
	Expect(time.Now().Before(deadline)).To(BeTrue(), "The probes did not complete before the deadline.")
}

// modelCall calls the mock model through the gateway with key. It returns the HTTP status and the
// error type of the answer. It does not print the key or the response body.
func modelCall(c context.Context, key string) (int, string) {
	body := `{"model":"` + mockModel + `","messages":[{"role":"user","content":"probe"}]}`
	req, err := http.NewRequestWithContext(c, http.MethodPost, gatewayURL+"/v1/chat/completions",
		strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "The gateway did not answer.")
	defer func() { _ = resp.Body.Close() }()
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return resp.StatusCode, e.Error.Type
}

// tokenClient is a client that uses only token. It has no client certificate.
func tokenClient(token string) *kubernetes.Clientset {
	cfg := rest.AnonymousClientConfig(restCfg)
	cfg.BearerToken = token
	cfg.Timeout = callTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	return cs
}

// canListPods sends a SelfSubjectAccessReview with token, as "kubectl auth can-i list pods" does.
func canListPods(c context.Context, token string) (bool, error) {
	ssar := &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authzv1.ResourceAttributes{Namespace: runNS, Verb: "list", Resource: "pods"},
	}}
	res, err := tokenClient(token).AuthorizationV1().SelfSubjectAccessReviews().Create(c, ssar,
		metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

// gatewayKey is the part of the gateway /key/info answer that the test reads.
type gatewayKey struct {
	Metadata map[string]any `json:"metadata"`
	Blocked  bool           `json:"blocked"`
}

// keyInfo reads the key with keyHash from the gateway with the master key.
func keyInfo(keyHash string) gatewayKey {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/key/info?key="+url.QueryEscape(keyHash), nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+masterKey)
	resp, err := httpClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "The gateway did not answer.")
	defer func() { _ = resp.Body.Close() }()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	var ki struct {
		Info gatewayKey `json:"info"`
	}
	Expect(json.NewDecoder(resp.Body).Decode(&ki)).To(Succeed())
	return ki.Info
}

// largestCall returns the largest spend of one call with the key, from the gateway spend logs.
func largestCall(keyHash string) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/spend/logs?api_key="+keyHash, nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+masterKey)
	resp, err := httpClient.Do(req)
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
