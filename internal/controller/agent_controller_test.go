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

package controller

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// fakeGateway records the calls. At each BlockKey it checks that the run RoleBinding still
// exists, to show that the controller blocks the key before it deletes anything.
type fakeGateway struct {
	mu      sync.Mutex
	created []gateway.KeySpec
	blocked []string
	order   []string
	// accessAtBlock is true if the RoleBinding existed at the first BlockKey.
	accessAtBlock bool
	runBinding    types.NamespacedName
	// usageErr is the Usage error.
	usageErr error
}

func (g *fakeGateway) CreateKey(_ context.Context, s gateway.KeySpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.created = append(g.created, s)
	g.order = append(g.order, "create")
	return nil
}

func (g *fakeGateway) RenewKey(context.Context, gateway.KeySpec) error { return nil }

func (g *fakeGateway) BlockKey(ctx context.Context, key string) error {
	err := k8sClient.Get(ctx, g.runBinding, &rbacv1.RoleBinding{})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.blocked) == 0 {
		g.accessAtBlock = err == nil
	}
	g.blocked = append(g.blocked, key)
	g.order = append(g.order, "block")
	return nil
}

func (g *fakeGateway) Usage(context.Context, string) (gateway.Usage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.order = append(g.order, "usage")
	if g.usageErr != nil {
		return gateway.Usage{}, g.usageErr
	}
	return gateway.Usage{Spend: 0.25, Calls: 3, FailedCalls: 1, InputTokens: 100, OutputTokens: 20}, nil
}

// endRunJob writes an end status to the run Job, as the Job controller does. envtest has no Job controller.
func endRunJob(ctx context.Context, run types.NamespacedName, t batchv1.JobConditionType) {
	job := &batchv1.Job{}
	Expect(k8sClient.Get(ctx, run, job)).To(Succeed())
	now := metav1.Now()
	if job.Status.StartTime == nil {
		// The API server refuses a change to a start time that is set.
		job.Status.StartTime = &now
	}
	if t == batchv1.JobComplete {
		job.Status.CompletionTime = &now
		job.Status.Succeeded = 1
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now})
	} else {
		job.Status.Failed = 1
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now})
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type: t, Status: corev1.ConditionTrue, LastTransitionTime: now})
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

// The names in the harness tests.
const (
	codexName      = "codex"
	claudeName     = "claude-code"
	pathEnv        = "PATH"
	installerImage = "ghcr.io/mohanraj00/koodam:test"
	installerCmd   = "/harness-install"
)

// installArgs is the init container command that the controller must make.
func installArgs(name, version, libc string) []string {
	return []string{installerCmd, "-harness", name, "-version", version, "-libc", libc, "-dir", harnessDir}
}

var _ = Describe("Agent Controller", func() {
	const ns = "default"
	var (
		ctx  context.Context
		logs *bytes.Buffer
		gw   *fakeGateway
		name string
		n    int
	)
	key := func() types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }
	run := func() types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: "kd-" + name} }
	newReconciler := func() *AgentReconciler {
		return &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Gateway: gw, GatewayURL: "http://gw.test:4000", Grace: time.Minute, JobTTL: 3600}
	}
	reconcileOnce := func(r *AgentReconciler) {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
	}
	agent := func() *corev1alpha1.Agent {
		a := &corev1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, key(), a)).To(Succeed())
		return a
	}
	exists := func(o client.Object) bool {
		err := k8sClient.Get(ctx, run(), o)
		if apierrors.IsNotFound(err) {
			return false
		}
		Expect(err).NotTo(HaveOccurred())
		return true
	}
	endJob := func(t batchv1.JobConditionType) { endRunJob(ctx, run(), t) }
	expectRevoked := func(phase corev1alpha1.AgentPhase) {
		Expect(gw.accessAtBlock).To(BeTrue(), "the key must be blocked before the access is deleted")
		Expect(gw.blocked).To(Equal([]string{gateway.HashKey(gw.created[0].Value)}))
		Expect(gw.order).To(Equal([]string{"create", "block", "usage"}))
		Expect(exists(&rbacv1.RoleBinding{})).To(BeFalse())
		Expect(exists(&rbacv1.Role{})).To(BeFalse())
		Expect(exists(&corev1.ServiceAccount{})).To(BeFalse())
		Expect(exists(&corev1.Secret{})).To(BeFalse())
		if phase == "" {
			return
		}
		a := agent()
		Expect(a.Status.Phase).To(Equal(phase))
		Expect(a.Status.RevokedAt).NotTo(BeNil())
		Expect(a.Status.Usage).To(Equal(&corev1alpha1.AgentUsage{
			Spend: "0.25", Calls: 3, FailedCalls: 1, InputTokens: 100, OutputTokens: 20}))
		Expect(meta.IsStatusConditionTrue(a.Status.Conditions, condRevoked)).To(BeTrue())
		Expect(a.Finalizers).NotTo(ContainElement(finalizer))
	}

	BeforeEach(func() {
		n++
		name = "run-" + strconv.Itoa(n)
		logs = &bytes.Buffer{}
		ctx = logr.NewContext(context.Background(), funcr.New(func(p, a string) {
			logs.WriteString(p + " " + a + "\n")
		}, funcr.Options{Verbosity: 10}))
		gw = &fakeGateway{runBinding: run()}
		Expect(k8sClient.Create(ctx, &corev1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AgentSpec{
				Image: "registry.k8s.io/pause:3.10", Command: []string{"/pause"}, Model: "local-agent",
				Budget: resource.MustParse("1.5"), DeadlineSeconds: 600, Env: map[string]string{"TASK": "t1"},
				Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{resPods}, Verbs: []string{"list"}}},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		for _, v := range gw.created {
			// Use booleans: a matcher with the key would show the key in the failure message.
			Expect(strings.Contains(logs.String(), v.Value)).To(BeFalse(), "a key value is in the log")
		}
		events := &corev1.EventList{}
		Expect(k8sClient.List(ctx, events, client.InNamespace(ns))).To(Succeed())
		for _, e := range events.Items {
			for _, v := range gw.created {
				Expect(strings.Contains(e.Message, v.Value)).To(BeFalse(), "a key value is in an event")
			}
		}
	})

	It("provisions a key, the access and a Job", func() {
		reconcileOnce(newReconciler())

		a := agent()
		Expect(a.Finalizers).To(ContainElement(finalizer))
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentRunning))
		Expect(a.Status.KeyAlias).To(Equal("kd-" + string(a.UID)))
		Expect(a.Status.KeyHash).To(Equal(gateway.HashKey(gw.created[0].Value)))
		Expect(meta.IsStatusConditionTrue(a.Status.Conditions, condProvisioned)).To(BeTrue())

		Expect(gw.created).To(HaveLen(1))
		s := gw.created[0]
		Expect(s.Value).To(HavePrefix("sk-kd-"))
		Expect(s.Alias).To(Equal(a.Status.KeyAlias))
		Expect(s.Model).To(Equal("local-agent"))
		Expect(s.Budget).To(Equal(1.5))
		Expect(s.TTL).To(Equal(11 * time.Minute))
		Expect(s.Metadata).NotTo(HaveKey("harness"))

		sec := &corev1.Secret{}
		Expect(exists(sec)).To(BeTrue())
		Expect(*sec.Immutable).To(BeTrue())
		Expect(string(sec.Data["ANTHROPIC_AUTH_TOKEN"])).To(Equal(s.Value))
		Expect(string(sec.Data["OPENAI_API_KEY"])).To(Equal(s.Value))
		Expect(string(sec.Data["OPENAI_BASE_URL"])).To(Equal("http://gw.test:4000/v1"))
		for _, v := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			Expect(string(sec.Data[v])).To(Equal("local-agent"), v)
		}
		Expect(exists(&corev1.ServiceAccount{})).To(BeTrue())
		role := &rbacv1.Role{}
		Expect(exists(role)).To(BeTrue())
		Expect(role.Rules).To(Equal(a.Spec.Rules))
		Expect(exists(&rbacv1.RoleBinding{})).To(BeTrue())

		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		Expect(*job.Spec.BackoffLimit).To(BeZero())
		Expect(*job.Spec.ActiveDeadlineSeconds).To(Equal(int64(600)))
		Expect(job.Spec.TTLSecondsAfterFinished).To(BeNil(), "the controller deletes the Job, not Kubernetes")
		c := job.Spec.Template.Spec.Containers[0]
		Expect(c.EnvFrom[0].SecretRef.Name).To(Equal("kd-" + name))
		Expect(c.Env).To(Equal([]corev1.EnvVar{{Name: "TASK", Value: "t1"}}))
		Expect(job.Spec.Template.Spec.InitContainers).To(BeEmpty(), "no harness, so no installer")
		Expect(job.Spec.Template.Spec.Volumes).To(BeEmpty())
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(RunLabel, "kd-"+name))
		// The suite BeforeEach binds the namespace "default" to testProfile.
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(UserLabel, testProfile))
		for _, o := range []client.Object{sec, role, job} {
			Expect(o.GetLabels()).To(HaveKeyWithValue(agentLabel, name))
			Expect(metav1.IsControlledBy(o, a)).To(BeTrue())
		}

		By("doing nothing more while the Job runs")
		reconcileOnce(newReconciler())
		Expect(gw.created).To(HaveLen(1))
		Expect(gw.blocked).To(BeEmpty())
	})

	setHarness := func(h *corev1alpha1.HarnessSpec, env map[string]string) {
		a := agent()
		a.Spec.Harness = h
		if env != nil {
			a.Spec.Env = env
		}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
	}

	It("adds an init container that installs the harness", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: claudeName, Version: "2.1.3"}, nil)
		r := newReconciler()
		r.InstallerImage = installerImage
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		pod := job.Spec.Template.Spec
		Expect(pod.Volumes).To(HaveLen(1))
		Expect(pod.Volumes[0].EmptyDir).NotTo(BeNil())
		Expect(pod.InitContainers).To(HaveLen(1))
		ic := pod.InitContainers[0]
		Expect(ic.Image).To(Equal(installerImage))
		Expect(gw.created[0].Metadata).To(HaveKeyWithValue("harness", claudeName))
		Expect(ic.Command).To(Equal(installArgs(claudeName, "2.1.3", "glibc")), "the CRD default for libc is glibc")
		Expect(ic.Env).To(BeEmpty())
		Expect(ic.EnvFrom).To(BeEmpty(), "the installer gets no credential")
		Expect(*ic.SecurityContext.RunAsNonRoot).To(BeTrue())
		Expect(*ic.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(*ic.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(ic.VolumeMounts).To(Equal([]corev1.VolumeMount{{Name: pod.Volumes[0].Name, MountPath: harnessDir}}))
		c := pod.Containers[0]
		Expect(c.VolumeMounts).To(Equal([]corev1.VolumeMount{
			{Name: pod.Volumes[0].Name, MountPath: harnessDir, ReadOnly: true}}))
		Expect(c.Env).To(ContainElement(corev1.EnvVar{Name: pathEnv, Value: harnessPath}))
		Expect(harnessPath).To(HavePrefix("/koodam/bin:"))
	})

	It("keeps the PATH from the Agent env", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: codexName}, map[string]string{pathEnv: "/opt/bin:/koodam/bin"})
		r := newReconciler()
		r.InstallerImage = installerImage
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		Expect(job.Spec.Template.Spec.InitContainers[0].Command).To(Equal(installArgs(codexName, "", "glibc")),
			"an empty version is the latest version")
		Expect(job.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{
			{Name: pathEnv, Value: "/opt/bin:/koodam/bin"}}))
	})

	It("gives Codex a model provider for the gateway", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: codexName}, nil)
		r := newReconciler()
		r.InstallerImage = installerImage
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		cfg := job.Spec.Template.Annotations[codexConfigAnnotation]
		Expect(cfg).To(Equal(`model = "local-agent"
model_provider = "koodam"

[model_providers.koodam]
name = "koodam"
base_url = "http://gw.test:4000/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
`))
		Expect(cfg).NotTo(ContainSubstring(gw.created[0].Value), "the config has the env name, not the key")
		pod := job.Spec.Template.Spec
		Expect(pod.Volumes).To(HaveLen(2))
		v := pod.Volumes[1]
		Expect(v.DownwardAPI).NotTo(BeNil())
		Expect(v.DownwardAPI.Items).To(HaveLen(1))
		Expect(v.DownwardAPI.Items[0].Path).To(Equal("config.toml"))
		Expect(v.DownwardAPI.Items[0].FieldRef.FieldPath).To(Equal("metadata.annotations['koodam.dev/codex-config']"))
		Expect(pod.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: v.Name, MountPath: "/etc/codex/config.toml", SubPath: "config.toml", ReadOnly: true}))
		Expect(pod.InitContainers[0].VolumeMounts).To(HaveLen(1), "the installer does not get the config")
	})

	It("gives no Codex config to Claude Code", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: claudeName}, nil)
		r := newReconciler()
		r.InstallerImage = installerImage
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		Expect(job.Spec.Template.Annotations).NotTo(HaveKey(codexConfigAnnotation))
		Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(1))
	})

	It("quotes the TOML strings in the Codex config", func() {
		Expect(tomlString(`a"b\c` + "\n\x7f")).To(Equal(`"a\"b\\c\u000A\u007F"`))
		Expect(codexConfig("http://gw", `m"`)).To(HavePrefix(`model = "m\""` + "\n"))
	})

	It("gives the musl libc to the installer", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: claudeName, Libc: "musl"}, nil)
		r := newReconciler()
		r.InstallerImage = installerImage
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		Expect(job.Spec.Template.Spec.InitContainers[0].Command).To(Equal(installArgs(claudeName, "", "musl")))
	})

	It("fails an Agent with a harness if the controller has no installer image", func() {
		setHarness(&corev1alpha1.HarnessSpec{Name: codexName}, nil)
		reconcileOnce(newReconciler())
		a := agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		c := meta.FindStatusCondition(a.Status.Conditions, condProvisioned)
		Expect(c).NotTo(BeNil())
		Expect(c.Reason).To(Equal("Invalid"))
		Expect(gw.created).To(BeEmpty(), "no key for an Agent that cannot run")
		Expect(exists(&batchv1.Job{})).To(BeFalse())
	})

	setTemplate := func(c corev1.Container) {
		a := agent()
		a.Spec.PodTemplate = &corev1alpha1.PodTemplate{
			Metadata: corev1alpha1.PodTemplateMetadata{Labels: map[string]string{"team": "a"}},
			Spec:     corev1.PodSpec{RuntimeClassName: new("gvisor"), Containers: []corev1.Container{c}},
		}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
	}

	It("merges spec.podTemplate into the run Job", func() {
		setTemplate(corev1.Container{Name: agentContainer, Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}})
		reconcileOnce(newReconciler())
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		t := job.Spec.Template
		Expect(t.Spec.RuntimeClassName).To(HaveValue(Equal("gvisor")))
		Expect(t.Labels).To(HaveKeyWithValue("team", "a"))
		Expect(t.Labels).To(HaveKeyWithValue(RunLabel, "kd-"+name))
		Expect(t.Spec.Containers).To(HaveLen(1))
		Expect(t.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("1Gi"))
		Expect(t.Spec.Containers[0].EnvFrom[0].SecretRef.Name).To(Equal("kd-" + name))
	})

	It("sends the run pod DNS to the egress proxy over spec.podTemplate", func() {
		a := agent()
		a.Spec.PodTemplate = &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{DNSPolicy: corev1.DNSDefault}}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		r := newReconciler()
		r.Egress = testEgress
		reconcileOnce(r)
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue())
		Expect(job.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSNone))
		Expect(job.Spec.Template.Spec.DNSConfig.Nameservers).To(Equal([]string{testEgress.IP}))
		c := meta.FindStatusCondition(agent().Status.Conditions, condTracedEgress)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
	})

	It("fails an Agent with a spec.podTemplate that the merge refuses, and makes no key", func() {
		setTemplate(corev1.Container{Name: agentContainer, Ports: []corev1.ContainerPort{{ContainerPort: 80}}})
		reconcileOnce(newReconciler())
		a := agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		c := meta.FindStatusCondition(a.Status.Conditions, condProvisioned)
		Expect(c).NotTo(BeNil())
		Expect(c.Reason).To(Equal(reasonInvalidPodTemplate))
		Expect(c.Message).To(ContainSubstring("spec.podTemplate.spec.containers"))
		Expect(gw.created).To(BeEmpty())
		Expect(exists(&batchv1.Job{})).To(BeFalse())
	})

	It("refuses a harness version that is not of the form 1.2.3", func() {
		a := agent()
		a.Spec.Harness = &corev1alpha1.HarnessSpec{Name: codexName, Version: "1.2.3; rm -rf /"}
		Expect(k8sClient.Update(ctx, a)).NotTo(Succeed())
		a = agent()
		a.Spec.Harness = &corev1alpha1.HarnessSpec{Name: "aider"}
		Expect(k8sClient.Update(ctx, a)).NotTo(Succeed())
		a = agent()
		a.Spec.Harness = &corev1alpha1.HarnessSpec{Name: codexName, Libc: "uclibc"}
		Expect(k8sClient.Update(ctx, a)).NotTo(Succeed())
	})

	It("revokes when the Job ends", func() {
		r := newReconciler()
		reconcileOnce(r)
		endJob(batchv1.JobComplete)
		reconcileOnce(r)
		expectRevoked(corev1alpha1.AgentSucceeded)
		Expect(exists(&batchv1.Job{})).To(BeTrue(), "the Job stays for its logs")

		By("doing nothing after the revoke")
		reconcileOnce(r)
		Expect(gw.blocked).To(HaveLen(1))
	})

	It("deletes the Job of a revoked run after the TTL from the end time", func() {
		r := newReconciler()
		reconcileOnce(r)
		endJob(batchv1.JobComplete)
		reconcileOnce(r)
		expectRevoked(corev1alpha1.AgentSucceeded)

		By("keeping the Job and requeueing before the TTL")
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically("~", time.Hour, time.Minute))
		Expect(exists(&batchv1.Job{})).To(BeTrue())

		By("using the default TTL when the reconciler has no TTL")
		r.JobTTL = 0
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically("~", DefaultJobTTLSeconds*time.Second, time.Minute))

		By("using revokedAt when there is no completedAt")
		r.JobTTL = 3600
		a := agent()
		a.Status.CompletedAt = nil
		a.Status.RevokedAt = &metav1.Time{Time: time.Now().Add(-30 * time.Minute)}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically("~", 30*time.Minute, time.Minute))

		By("deleting the Job when the TTL is in the past")
		a = agent()
		a.Status.CompletedAt = &metav1.Time{Time: time.Now().Add(-25 * time.Hour)}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeZero())
		Expect(exists(&batchv1.Job{})).To(BeFalse())
		Expect(agent().Status.Phase).To(Equal(corev1alpha1.AgentSucceeded), "the result stays")

		By("doing nothing and not requeueing when the Job is gone, also before the TTL")
		a = agent()
		a.Status.CompletedAt = &metav1.Time{Time: time.Now()}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeZero())
		Expect(gw.blocked).To(HaveLen(1))
	})

	It("revokes and reads the spend when a different tool deleted a finished Job", func() {
		r := newReconciler()
		reconcileOnce(r)
		By("ending the Job and deleting it before the controller sees the end")
		endJob(batchv1.JobComplete)
		Expect(k8sClient.Delete(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns}},
			client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		reconcileOnce(r)
		// expectRevoked checks the gateway call order: the block comes before the usage read.
		expectRevoked(corev1alpha1.AgentFailed)
		Expect(meta.FindStatusCondition(agent().Status.Conditions, condRevoked).Reason).To(Equal("JobMissing"))
	})

	It("revokes a failed Job as Failed", func() {
		r := newReconciler()
		reconcileOnce(r)
		endJob(batchv1.JobFailed)
		reconcileOnce(r)
		expectRevoked(corev1alpha1.AgentFailed)
	})

	It("writes the start time and the end time of the Job one time only", func() {
		r := newReconciler()
		reconcileOnce(r)
		Expect(agent().Status.StartedAt).To(BeNil())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, run(), job)).To(Succeed())
		jobStart := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
		job.Status.StartTime = &jobStart
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
		reconcileOnce(r)
		a := agent()
		Expect(a.Status.StartedAt).NotTo(BeNil())
		Expect(a.Status.StartedAt.Equal(&jobStart)).To(BeTrue())
		Expect(a.Status.CompletedAt).To(BeNil())

		By("keeping a start time that is set when it is not the Job start time")
		start := metav1.NewTime(jobStart.Add(-time.Minute))
		a.Status.StartedAt = &start
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		reconcileOnce(r)
		Expect(agent().Status.StartedAt.Equal(&start)).To(BeTrue())

		endJob(batchv1.JobComplete)
		Expect(k8sClient.Get(ctx, run(), job)).To(Succeed())
		reconcileOnce(r)
		expectRevoked(corev1alpha1.AgentSucceeded)
		a = agent()
		Expect(a.Status.StartedAt.Equal(&start)).To(BeTrue())
		Expect(a.Status.CompletedAt).NotTo(BeNil())
		Expect(a.Status.CompletedAt.Equal(job.Status.CompletionTime)).To(BeTrue())

		By("keeping both times on a later reconcile")
		done := *a.Status.CompletedAt
		reconcileOnce(r)
		a = agent()
		Expect(a.Status.StartedAt.Equal(&start)).To(BeTrue())
		Expect(a.Status.CompletedAt.Equal(&done)).To(BeTrue())
	})

	It("writes the time of the Failed condition as the end time of a failed Job", func() {
		r := newReconciler()
		reconcileOnce(r)
		endJob(batchv1.JobFailed)
		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, run(), job)).To(Succeed())
		reconcileOnce(r)
		a := agent()
		Expect(a.Status.StartedAt.Equal(job.Status.StartTime)).To(BeTrue())
		Expect(a.Status.CompletedAt).NotTo(BeNil())
		failed := job.Status.Conditions[len(job.Status.Conditions)-1]
		Expect(failed.Type).To(Equal(batchv1.JobFailed))
		Expect(a.Status.CompletedAt.Equal(&failed.LastTransitionTime)).To(BeTrue())
	})

	It("revokes when the Agent is deleted during a run", func() {
		r := newReconciler()
		reconcileOnce(r)
		Expect(k8sClient.Delete(ctx, agent())).To(Succeed())
		reconcileOnce(r)
		expectRevoked("")
		Expect(exists(&batchv1.Job{})).To(BeFalse(), "the Job of an active run is deleted")
		err := k8sClient.Get(ctx, key(), &corev1alpha1.Agent{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the finalizer is removed")
	})

	It("revokes after a controller restart during a run", func() {
		reconcileOnce(newReconciler())
		endJob(batchv1.JobComplete)
		// A new reconciler has no memory of the run. It reads the key hash from the status.
		reconcileOnce(newReconciler())
		expectRevoked(corev1alpha1.AgentSucceeded)
	})

	It("fails a rule outside the ceiling before it makes a key", func() {
		a := agent()
		a.Spec.Rules = []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{resSecrets}, Verbs: []string{verbGet}}}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		reconcileOnce(newReconciler())
		a = agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		Expect(meta.FindStatusCondition(a.Status.Conditions, condRevoked).Reason).To(Equal("Invalid"))
		Expect(gw.created).To(BeEmpty())
		Expect(exists(&corev1.Secret{})).To(BeFalse())
		Expect(a.Finalizers).NotTo(ContainElement(finalizer))
	})

	It("keeps an object with the run name from a different owner", func() {
		other := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns},
			Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{resSecrets}, Verbs: []string{verbGet}}}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		reconcileOnce(newReconciler())
		a := agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		Expect(meta.FindStatusCondition(a.Status.Conditions, condRevoked).Reason).To(Equal("NameConflict"))
		Expect(gw.blocked).To(Equal([]string{a.Status.KeyHash}))
		role := &rbacv1.Role{}
		Expect(exists(role)).To(BeTrue(), "the controller does not delete an object that it does not own")
		Expect(role.UID).To(Equal(other.UID))
		Expect(exists(&rbacv1.RoleBinding{})).To(BeFalse())
		Expect(exists(&batchv1.Job{})).To(BeFalse())
		Expect(exists(&corev1.ServiceAccount{})).To(BeFalse())
		Expect(exists(&corev1.Secret{})).To(BeFalse())
	})

	It("revokes by the status hash when the Secret is gone", func() {
		r := newReconciler()
		reconcileOnce(r)
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns}})).
			To(Succeed())
		endJob(batchv1.JobComplete)
		reconcileOnce(r)
		expectRevoked(corev1alpha1.AgentSucceeded)
	})

	It("continues a revoke that stopped", func() {
		r := newReconciler()
		reconcileOnce(r)
		endJob(batchv1.JobComplete)
		gw.usageErr = errors.New("gateway down")
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		Expect(err).To(HaveOccurred())
		a := agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentSucceeded), "the end phase is written before the revoke")
		Expect(a.Finalizers).To(ContainElement(finalizer))

		gw.usageErr = nil
		reconcileOnce(r)
		a = agent()
		Expect(meta.IsStatusConditionTrue(a.Status.Conditions, condRevoked)).To(BeTrue())
		Expect(a.Finalizers).NotTo(ContainElement(finalizer))
		Expect(exists(&corev1.Secret{})).To(BeFalse())
		Expect(gw.created).To(HaveLen(1), "a retry does not provision again")
	})

	It("completes the revoke when the gateway has no key", func() {
		r := newReconciler()
		reconcileOnce(r)
		gw.usageErr = gateway.ErrKeyNotFound
		Expect(k8sClient.Delete(ctx, agent())).To(Succeed())
		reconcileOnce(r)
		err := k8sClient.Get(ctx, key(), &corev1alpha1.Agent{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the finalizer is removed")
	})

	It("fails the run when a different Job has the run name", func() {
		r := newReconciler()
		reconcileOnce(r)
		Expect(k8sClient.Delete(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns}},
			client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		other := runJob(agent(), "", "")
		other.OwnerReferences = nil
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		reconcileOnce(r)
		a := agent()
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		Expect(meta.FindStatusCondition(a.Status.Conditions, condRevoked).Reason).To(Equal("NameConflict"))
		Expect(gw.blocked).To(Equal([]string{a.Status.KeyHash}))
		job := &batchv1.Job{}
		Expect(exists(job)).To(BeTrue(), "the controller does not delete a Job that it does not own")
		Expect(job.UID).To(Equal(other.UID))

		By("keeping the Job of a different owner after the TTL")
		a = agent()
		a.Status.RevokedAt = &metav1.Time{Time: time.Now().Add(-2 * time.Hour)}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		reconcileOnce(r)
		Expect(exists(&batchv1.Job{})).To(BeTrue())
	})
})
