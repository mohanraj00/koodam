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
	"reflect"
	"slices"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
)

const (
	// finalizer keeps the Agent until the controller revokes the key.
	finalizer = "koodam.dev/revoke"
	// agentLabel is on each object that the controller makes for an Agent.
	agentLabel = "koodam.dev/agent"
	// keyField is the Secret field with the key value.
	keyField = "ANTHROPIC_AUTH_TOKEN"

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
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.koodam.dev,resources=agents/status,verbs=get;update;patch
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
		if phase, reason, done := jobEnd(job); done {
			return ctrl.Result{}, r.fail(ctx, a, phase, reason)
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
	if msg := invalid(a); msg != "" {
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
	err = r.Gateway.CreateKey(ctx, gateway.KeySpec{
		Value: key, Alias: alias, Model: a.Spec.Model, Budget: a.Spec.Budget.AsApproximateFloat64(),
		TTL: time.Duration(a.Spec.DeadlineSeconds)*time.Second + r.Grace,
		Metadata: map[string]string{
			"uid": string(a.UID), "namespace": a.Namespace, "agent": a.Name, "user": a.Spec.User,
		},
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
		runJob(a),
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
		case apierrors.IsForbidden(err), apierrors.IsInvalid(err):
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

// secret has the env for the harnesses. Stage 2 found that a missing model alias gives a 403.
func (r *AgentReconciler) secret(a *corev1alpha1.Agent, key string) *corev1.Secret {
	m := a.Spec.Model
	return &corev1.Secret{ObjectMeta: runMeta(a), Immutable: new(true), StringData: map[string]string{
		"ANTHROPIC_BASE_URL": r.GatewayURL, keyField: key, "ANTHROPIC_MODEL": m,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": m, "ANTHROPIC_DEFAULT_SONNET_MODEL": m,
		"ANTHROPIC_DEFAULT_OPUS_MODEL": m, "ANTHROPIC_SMALL_FAST_MODEL": m, "CLAUDE_CODE_SUBAGENT_MODEL": m,
		"OPENAI_BASE_URL": r.GatewayURL + "/v1", "OPENAI_API_KEY": key,
	}}
}

func runJob(a *corev1alpha1.Agent) *batchv1.Job {
	name := runName(a)
	env := make([]corev1.EnvVar, 0, len(a.Spec.Env))
	for k, v := range a.Spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	return &batchv1.Job{ObjectMeta: runMeta(a), Spec: batchv1.JobSpec{
		BackoffLimit:          new(int32(0)),
		ActiveDeadlineSeconds: new(a.Spec.DeadlineSeconds),
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{agentLabel: a.Name}},
			Spec: corev1.PodSpec{
				RestartPolicy:      corev1.RestartPolicyNever,
				ServiceAccountName: name,
				Containers: []corev1.Container{{
					Name: "agent", Image: a.Spec.Image, Command: a.Spec.Command, Env: env,
					EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
				}},
			},
		},
	}}
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

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Agent{}).
		Owns(&batchv1.Job{}).
		Named("agent").
		Complete(r)
}
