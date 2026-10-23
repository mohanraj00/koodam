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
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/harness"
)

const (
	// finalizer keeps the Agent until the controller revokes the key.
	finalizer = "koodam.dev/revoke"
	// agentLabel is on each object that the controller makes for an Agent.
	agentLabel = "koodam.dev/agent"
	// keyField is the Secret field with the key value.
	keyField = "ANTHROPIC_AUTH_TOKEN"
	// openAIKeyField is the Secret field with the key value for Codex.
	openAIKeyField = "OPENAI_API_KEY"
	// modelField is the Secret field with the model name.
	modelField = "ANTHROPIC_MODEL"

	// DefaultJobTTLSeconds is the default time that the controller keeps a run Job after it ends (24 h).
	DefaultJobTTLSeconds = 86400
	// MinJobTTLSeconds is the minimum Job TTL. It gives a user the time to read the pod log.
	MinJobTTLSeconds = 60

	condProvisioned = "Provisioned"
	condRevoked     = "Revoked"
)

// ceiling is the most that a run Role can grant. It must match config/rbac/agent_ceiling_role.yaml.
// The API server compares a Role with all the permissions of the controller, which are wider than
// the ceiling. Thus the controller compares the rules with the ceiling before it makes a key.
var ceiling = rbacv1.PolicyRule{
	APIGroups: []string{""},
	Resources: []string{"pods", "pods/log", "configmaps", "events"},
	Verbs:     []string{"get", "list", "watch"},
}

// errNotOwned is the error when an object with the run name is not from this Agent.
var errNotOwned = errors.New("an object with the run name is not from this Agent")

// AgentReconciler reconciles an Agent object. It keeps no state in memory. The status has the key
// hash, so a restart of the controller can revoke a run that it did not provision.
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the run objects directly, so the controller does not cache all of them.
	APIReader client.Reader
	Gateway   gateway.Gateway
	// GatewayURL is the gateway address for the agent pods.
	GatewayURL string
	// Grace is added to deadlineSeconds to give the key TTL.
	Grace time.Duration
	// InstallerImage has /harness-install. The run pod uses it as an init container if the Agent
	// has a harness. If it is empty, an Agent with a harness fails.
	InstallerImage string
	// JobTTL is the time in seconds that the controller keeps a run Job and its pods after the Job
	// ends. If it is 0 or less, the controller uses DefaultJobTTLSeconds.
	JobTTL int32
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=agents,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=agents/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;create;delete

// Reconcile provisions a run for a new Agent. It revokes the run when the Job ends, when the
// provision fails, or when the Agent is deleted.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a := &corev1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	ended := a.Status.Phase == corev1alpha1.AgentSucceeded || a.Status.Phase == corev1alpha1.AgentFailed
	if (ended || !a.DeletionTimestamp.IsZero()) && !controllerutil.ContainsFinalizer(a, finalizer) {
		if ended && a.DeletionTimestamp.IsZero() {
			return r.deleteEndedJob(ctx, a)
		}
		return ctrl.Result{}, nil
	}
	switch {
	case !a.DeletionTimestamp.IsZero():
		return ctrl.Result{}, r.fail(ctx, a, corev1alpha1.AgentFailed, "AgentDeleted")
	case ended:
		// A revoke started before and did not complete.
		return ctrl.Result{}, r.revoke(ctx, a)
	case a.Status.Phase == corev1alpha1.AgentRunning:
		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, job)
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.fail(ctx, a, corev1alpha1.AgentFailed, "JobMissing")
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if !metav1.IsControlledBy(job, a) {
			// A different Job has the run name. Its result is not the result of this run.
			return ctrl.Result{}, r.fail(ctx, a, corev1alpha1.AgentFailed, "NameConflict")
		}
		phase, reason, done := jobEnd(job)
		changed := setJobTimes(a, job)
		if done {
			// fail writes the times in the same status update as the end phase, before the revoke.
			return ctrl.Result{}, r.fail(ctx, a, phase, reason)
		}
		if changed {
			return ctrl.Result{}, r.Status().Update(ctx, a)
		}
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, r.provision(ctx, a)
	}
}

func runName(a *corev1alpha1.Agent) string { return "kd-" + a.Name }

func jobEnd(job *batchv1.Job) (corev1alpha1.AgentPhase, string, bool) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return corev1alpha1.AgentSucceeded, "JobComplete", true
		case batchv1.JobFailed:
			return corev1alpha1.AgentFailed, "JobFailed", true
		}
	}
	return "", "", false
}

// setJobTimes copies the start time and the end time of the Job to the Agent status. It sets
// each time one time only. It returns true if it changed the status.
func setJobTimes(a *corev1alpha1.Agent, job *batchv1.Job) bool {
	changed := false
	if a.Status.StartedAt == nil && job.Status.StartTime != nil {
		a.Status.StartedAt = job.Status.StartTime.DeepCopy()
		changed = true
	}
	if a.Status.CompletedAt == nil {
		if end := jobEndTime(job); end != nil {
			a.Status.CompletedAt = end
			changed = true
		}
	}
	return changed
}

// jobEndTime returns the completion time of the Job. For a failed Job, it returns the time of
// the Failed condition. It returns nil if the Job did not end.
func jobEndTime(job *batchv1.Job) *metav1.Time {
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.DeepCopy()
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.DeepCopy()
		}
	}
	return nil
}

// invalid returns a message if the controller cannot make the run. It does no API call.
func invalid(a *corev1alpha1.Agent) string {
	if len(runName(a)) > validation.DNS1123LabelMaxLength {
		return "The Agent name is too long for the run Job. Use 60 characters or fewer."
	}
	in := func(have, ceil []string) bool {
		return len(have) > 0 && !slices.ContainsFunc(have, func(s string) bool { return !slices.Contains(ceil, s) })
	}
	for _, rule := range a.Spec.Rules {
		if len(rule.NonResourceURLs) > 0 || !in(rule.APIGroups, ceiling.APIGroups) ||
			!in(rule.Resources, ceiling.Resources) || !in(rule.Verbs, ceiling.Verbs) {
			return "A rule is not in the agent-ceiling ClusterRole."
		}
	}
	return ""
}

// permanent is true for an API error that a retry cannot fix: a 4xx status other than a
// conflict, a timeout or a rate limit.
func permanent(err error) bool {
	var s apierrors.APIStatus
	if !errors.As(err, &s) {
		return false
	}
	code := s.Status().Code
	return code >= 400 && code < 500 && code != http.StatusConflict &&
		code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

func runMeta(a *corev1alpha1.Agent) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: runName(a), Namespace: a.Namespace,
		Labels: map[string]string{agentLabel: a.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(a,
			corev1alpha1.GroupVersion.WithKind("Agent"))},
	}
}

// getOwned reads the run object of this kind into o. It returns false if there is no object.
// It returns errNotOwned if the object is not controlled by this Agent.
func (r *AgentReconciler) getOwned(ctx context.Context, a *corev1alpha1.Agent, o client.Object) (bool, error) {
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, o)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(o, a) {
		return true, errNotOwned
	}
	return true, nil
}

// runKey reads the key value from the run Secret. It returns "" if there is no Secret.
func (r *AgentReconciler) runKey(ctx context.Context, a *corev1alpha1.Agent) (string, error) {
	s := &corev1.Secret{}
	found, err := r.getOwned(ctx, a, s)
	if !found || err != nil {
		return "", err
	}
	if len(s.Data[keyField]) == 0 {
		return "", errNotOwned
	}
	return string(s.Data[keyField]), nil
}

// sameRun is true if have, an object that exists with the run name, is the object that the
// controller makes for this Agent.
func (r *AgentReconciler) sameRun(ctx context.Context, a *corev1alpha1.Agent, want client.Object) (bool, error) {
	have := reflect.New(reflect.TypeOf(want).Elem()).Interface().(client.Object)
	if _, err := r.getOwned(ctx, a, have); errors.Is(err, errNotOwned) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	switch w := want.(type) {
	case *rbacv1.Role:
		return equality.Semantic.DeepEqual(w.Rules, have.(*rbacv1.Role).Rules), nil
	case *rbacv1.RoleBinding:
		h := have.(*rbacv1.RoleBinding)
		return w.RoleRef == h.RoleRef && equality.Semantic.DeepEqual(w.Subjects, h.Subjects), nil
	}
	return true, nil
}

func (r *AgentReconciler) provision(ctx context.Context, a *corev1alpha1.Agent) error {
	log := logf.FromContext(ctx)
	msg := invalid(a)
	if msg == "" && a.Spec.Harness != nil && r.InstallerImage == "" {
		msg = "The controller has no installer image, so it cannot install a harness."
	}
	if msg != "" {
		log.Info("The Agent is not valid", "reason", msg)
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type: condProvisioned, Status: metav1.ConditionFalse, Reason: "Invalid", Message: msg,
			ObservedGeneration: a.Generation,
		})
		return r.fail(ctx, a, corev1alpha1.AgentFailed, "Invalid")
	}
	if controllerutil.AddFinalizer(a, finalizer) {
		if err := r.Update(ctx, a); err != nil {
			return err
		}
	}
	// The Secret comes first. A retry after a crash finds the key value in it.
	key, err := r.runKey(ctx, a)
	switch {
	case errors.Is(err, errNotOwned):
		return r.fail(ctx, a, corev1alpha1.AgentFailed, "NameConflict")
	case err != nil:
		return err
	case a.Status.KeyHash != "" && gateway.HashKey(key) != a.Status.KeyHash:
		// The Secret is gone or changed. The revoke uses the hash in the status.
		return r.fail(ctx, a, corev1alpha1.AgentFailed, "SecretLost")
	case key == "":
		if key, err = gateway.NewKeyValue(); err != nil {
			return err
		}
		if err := r.Create(ctx, r.secret(a, key)); err != nil {
			return err
		}
	}
	alias := "kd-" + string(a.UID)
	if a.Status.KeyHash == "" {
		// Write the hash before the key exists, so that a revoke can always find the key.
		a.Status.Phase = corev1alpha1.AgentPending
		a.Status.KeyAlias = alias
		a.Status.KeyHash = gateway.HashKey(key)
		if err := r.Status().Update(ctx, a); err != nil {
			return err
		}
	}
	md := map[string]string{"uid": string(a.UID), "namespace": a.Namespace, "agent": a.Name, "user": a.Spec.User}
	if a.Spec.Harness != nil {
		// The gateway can then group the spend by harness.
		md["harness"] = a.Spec.Harness.Name
	}
	err = r.Gateway.CreateKey(ctx, gateway.KeySpec{
		Value: key, Alias: alias, Model: a.Spec.Model, Budget: a.Spec.Budget.AsApproximateFloat64(),
		TTL: time.Duration(a.Spec.DeadlineSeconds)*time.Second + r.Grace, Metadata: md,
	})
	if err != nil {
		return err
	}
	name := runName(a)
	objs := []client.Object{
		&corev1.ServiceAccount{ObjectMeta: runMeta(a)},
		&rbacv1.Role{ObjectMeta: runMeta(a), Rules: a.Spec.Rules},
		&rbacv1.RoleBinding{ObjectMeta: runMeta(a),
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: a.Namespace}},
		},
		runJob(a, r.InstallerImage, r.GatewayURL),
	}
	for _, o := range objs {
		err := r.Create(ctx, o)
		switch {
		case apierrors.IsAlreadyExists(err):
			same, err := r.sameRun(ctx, a, o)
			if err != nil {
				return err
			}
			if !same {
				log.Info("An object with the run name is not from this Agent", "kind", fmt.Sprintf("%T", o))
				return r.fail(ctx, a, corev1alpha1.AgentFailed, "NameConflict")
			}
		case permanent(err):
			log.Info("The API server refused the run", "kind", fmt.Sprintf("%T", o), "reason", err.Error())
			return r.fail(ctx, a, corev1alpha1.AgentFailed, "Refused")
		case err != nil:
			return err
		}
	}
	a.Status.Phase = corev1alpha1.AgentRunning
	a.Status.Job = name
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: condProvisioned, Status: metav1.ConditionTrue, Reason: "Provisioned",
		Message: "The key, the credentials and the Job are ready.", ObservedGeneration: a.Generation,
	})
	log.Info("Provisioned the run", "keyAlias", alias)
	return r.Status().Update(ctx, a)
}

// secret has the env for the harnesses.
func (r *AgentReconciler) secret(a *corev1alpha1.Agent, key string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: runMeta(a), Immutable: new(true),
		StringData: gatewayEnv(r.GatewayURL, a.Spec.Model, key)}
}

// gatewayEnv is the env of the harnesses for a gateway key. Stage 2 found that a missing model
// alias gives a 403.
func gatewayEnv(url, m, key string) map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL": url, keyField: key, modelField: m,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": m, "ANTHROPIC_DEFAULT_SONNET_MODEL": m,
		"ANTHROPIC_DEFAULT_OPUS_MODEL": m, "ANTHROPIC_SMALL_FAST_MODEL": m, "CLAUDE_CODE_SUBAGENT_MODEL": m,
		"OPENAI_BASE_URL": url + "/v1", openAIKeyField: key,
	}
}

// runJob makes the run Job. The Job has no ttlSecondsAfterFinished. If Kubernetes deletes the Job
// while the controller is down, the result of the run is lost. Thus deleteEndedJob deletes it.
func runJob(a *corev1alpha1.Agent, installerImage, gatewayURL string) *batchv1.Job {
	name := runName(a)
	env := make([]corev1.EnvVar, 0, len(a.Spec.Env)+1)
	for k, v := range a.Spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	pod := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: name,
		Containers: []corev1.Container{{
			Name: "agent", Image: a.Spec.Image, Command: a.Spec.Command, Env: env,
			EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
		}},
	}
	tmpl := metav1.ObjectMeta{Labels: map[string]string{agentLabel: a.Name}}
	if h := a.Spec.Harness; h != nil {
		addHarness(&pod, h, installerImage)
		if h.Name == harness.Codex {
			tmpl.Annotations = map[string]string{codexConfigAnnotation: codexConfig(gatewayURL, a.Spec.Model)}
			addCodexConfig(&pod)
		}
	}
	return &batchv1.Job{ObjectMeta: runMeta(a), Spec: batchv1.JobSpec{
		BackoffLimit:          new(int32(0)),
		ActiveDeadlineSeconds: new(a.Spec.DeadlineSeconds),
		Template:              corev1.PodTemplateSpec{ObjectMeta: tmpl, Spec: pod},
	}}
}

// codexConfigPath is the system config file of Codex. A config.toml in CODEX_HOME can change it.
const codexConfigPath = "/etc/codex/config.toml"

// codexConfigAnnotation is the pod annotation with the Codex config. It holds no credential.
const codexConfigAnnotation = "koodam.dev/codex-config"

// codexConfig is the Codex config for the gateway. Codex does not read OPENAI_BASE_URL, and it
// supports only the Responses API. The config has the name of the key env, not the key value.
func codexConfig(gatewayURL, model string) string {
	return "model = " + tomlString(model) + "\nmodel_provider = \"koodam\"\n\n" +
		"[model_providers.koodam]\nname = \"koodam\"\nbase_url = " + tomlString(strings.TrimSuffix(gatewayURL, "/")+"/v1") + "\n" +
		"env_key = " + tomlString(openAIKeyField) + "\nwire_api = \"responses\"\n"
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// addCodexConfig gives the pod annotation with the Codex config to the agent container as the
// file codexConfigPath. A downward API volume does this with no new object and no RBAC verb.
func addCodexConfig(pod *corev1.PodSpec) {
	const vol, file = "codex-config", "config.toml"
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{
		DownwardAPI: &corev1.DownwardAPIVolumeSource{Items: []corev1.DownwardAPIVolumeFile{{Path: file,
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.annotations['" + codexConfigAnnotation + "']"}}}}}})
	// The subPath mounts only the file, so the other files of /etc/codex in the image stay.
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: codexConfigPath,
		SubPath: file, ReadOnly: true})
}

// harnessDir is the folder of the harness binary in the run pod.
const harnessDir = "/koodam/bin"

// harnessPath is the PATH of the agent container if the Agent has a harness and no PATH in its env.
const harnessPath = harnessDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// addHarness adds an init container that downloads the harness binary into an emptyDir. The
// agent container mounts the emptyDir read-only. The init container has no credential env.
func addHarness(pod *corev1.PodSpec, h *corev1alpha1.HarnessSpec, image string) {
	const vol = "harness"
	limit := resource.MustParse("1Gi")
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}})
	pod.InitContainers = append(pod.InitContainers, installerContainer(vol, vol, h.Name, h.Version, h.Libc, image))
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: harnessDir, ReadOnly: true})
	if !slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == "PATH" }) {
		c.Env = append(c.Env, corev1.EnvVar{Name: "PATH", Value: harnessPath})
	}
}

// fail writes the end phase and the reason before the revoke. A retry then continues the revoke
// and does not provision again.
func (r *AgentReconciler) fail(ctx context.Context, a *corev1alpha1.Agent, phase corev1alpha1.AgentPhase,
	reason string) error {
	if a.Status.Phase != corev1alpha1.AgentSucceeded && a.Status.Phase != corev1alpha1.AgentFailed {
		a.Status.Phase = phase
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type: condRevoked, Status: metav1.ConditionFalse, Reason: reason,
			Message: "The revoke started.", ObservedGeneration: a.Generation,
		})
		if err := r.Status().Update(ctx, a); err != nil {
			return err
		}
	}
	return r.revoke(ctx, a)
}

// revoke blocks the key first, by the hash in the status. Then it deletes the access, reads the
// usage, writes the status, deletes the Secret and removes the finalizer. Each step can run again.
func (r *AgentReconciler) revoke(ctx context.Context, a *corev1alpha1.Agent) error {
	if a.Status.KeyHash != "" {
		if err := r.Gateway.BlockKey(ctx, a.Status.KeyHash); err != nil {
			return err
		}
	}
	reason := "Revoked"
	c := meta.FindStatusCondition(a.Status.Conditions, condRevoked)
	if c != nil {
		reason = c.Reason
	}
	var gone []client.Object
	if reason != "JobComplete" && reason != "JobFailed" {
		// Stop the pods of a run that did not end.
		gone = append(gone, &batchv1.Job{})
	}
	gone = append(gone, &rbacv1.RoleBinding{}, &rbacv1.Role{}, &corev1.ServiceAccount{})
	for _, o := range gone {
		if err := r.deleteOwned(ctx, a, o); err != nil {
			return err
		}
	}
	if c == nil || c.Status != metav1.ConditionTrue {
		if err := r.writeUsage(ctx, a, reason); err != nil {
			return err
		}
	}
	if err := r.deleteOwned(ctx, a, &corev1.Secret{}); err != nil {
		return err
	}
	if controllerutil.RemoveFinalizer(a, finalizer) {
		logf.FromContext(ctx).Info("Revoked the run", "keyAlias", a.Status.KeyAlias, "reason", reason)
		return r.Update(ctx, a)
	}
	return nil
}

// deleteOwned deletes the run object of this kind. It keeps an object that is not from this Agent.
// The UID precondition stops the delete if the object changed after the read.
func (r *AgentReconciler) deleteOwned(ctx context.Context, a *corev1alpha1.Agent, o client.Object) error {
	found, err := r.getOwned(ctx, a, o)
	if errors.Is(err, errNotOwned) {
		logf.FromContext(ctx).Info("Kept an object with the run name that is not from this Agent",
			"kind", fmt.Sprintf("%T", o))
		return nil
	}
	if !found || err != nil {
		return err
	}
	uid := o.GetUID()
	err = r.Delete(ctx, o, client.Preconditions{UID: &uid},
		client.PropagationPolicy(metav1.DeletePropagationBackground))
	return client.IgnoreNotFound(err)
}

func (r *AgentReconciler) writeUsage(ctx context.Context, a *corev1alpha1.Agent, reason string) error {
	msg := "The key is blocked and the credentials are deleted."
	if a.Status.KeyHash == "" {
		msg = "There was no key to revoke."
	} else {
		u, err := r.Gateway.Usage(ctx, a.Status.KeyHash)
		switch {
		case errors.Is(err, gateway.ErrKeyNotFound):
			msg = "The gateway had no key for this run."
		case errors.Is(err, gateway.ErrSpendNotSettled), err == nil:
			if err != nil {
				msg += " The spend did not settle, so the usage can be low."
			}
			a.Status.Usage = &corev1alpha1.AgentUsage{
				Spend: strconv.FormatFloat(u.Spend, 'f', -1, 64), Calls: u.Calls, FailedCalls: u.FailedCalls,
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			}
		default:
			return err
		}
	}
	a.Status.RevokedAt = &metav1.Time{Time: time.Now()}
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: condRevoked, Status: metav1.ConditionTrue, Reason: reason, Message: msg,
		ObservedGeneration: a.Generation,
	})
	return r.Status().Update(ctx, a)
}

// deleteEndedJob deletes the Job of a revoked run JobTTL seconds after the Job ends. The controller
// deletes the Job only after it recorded the result. If the end time is in the future, it requeues.
func (r *AgentReconciler) deleteEndedJob(ctx context.Context, a *corev1alpha1.Agent) (ctrl.Result, error) {
	// The cache has the run Jobs. Read the API server only if the cache has a Job of this Agent.
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, job)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(job, a) {
		return ctrl.Result{}, nil
	}
	// The revoke always sets revokedAt. completedAt is empty if the Job ended with no end time.
	end := a.Status.CompletedAt
	if end == nil {
		end = a.Status.RevokedAt
	}
	if end == nil {
		return ctrl.Result{}, nil
	}
	ttl := r.JobTTL
	if ttl <= 0 {
		ttl = DefaultJobTTLSeconds
	}
	if wait := time.Until(end.Add(time.Duration(ttl) * time.Second)); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	return ctrl.Result{}, r.deleteOwned(ctx, a, &batchv1.Job{})
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Agent{}).
		Owns(&batchv1.Job{}).
		Named("agent").
		Complete(r)
}
