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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	// keyField is the Secret field with the key value. The controller reads it to revoke.
	keyField = "ANTHROPIC_AUTH_TOKEN"

	condProvisioned = "Provisioned"
	condRevoked     = "Revoked"
)

// AgentReconciler reconciles an Agent object. It keeps no state in memory: the key value is in
// the run Secret, so a restart of the controller can revoke a run that it did not provision.
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the run Secret directly, so the controller does not cache all Secrets.
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
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=create;delete

// Reconcile provisions a run for a new Agent. It revokes the run when the Job ends or when the
// Agent is deleted.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a := &corev1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !a.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(a, finalizer) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, r.revoke(ctx, a, corev1alpha1.AgentFailed, "AgentDeleted")
	}
	switch a.Status.Phase {
	case "", corev1alpha1.AgentPending:
		return ctrl.Result{}, r.provision(ctx, a)
	case corev1alpha1.AgentRunning:
		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, job)
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.revoke(ctx, a, corev1alpha1.AgentFailed, "JobMissing")
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if phase, reason, done := jobEnd(job); done {
			return ctrl.Result{}, r.revoke(ctx, a, phase, reason)
		}
	}
	return ctrl.Result{}, nil
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

func newKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-kd-" + hex.EncodeToString(b), nil
}

func runMeta(a *corev1alpha1.Agent) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: runName(a), Namespace: a.Namespace,
		Labels: map[string]string{agentLabel: a.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(a,
			corev1alpha1.GroupVersion.WithKind("Agent"))},
	}
}

// runKey reads the key value from the run Secret. It returns "" if there is no Secret.
func (r *AgentReconciler) runKey(ctx context.Context, a *corev1alpha1.Agent) (string, error) {
	s := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: runName(a)}, s)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !metav1.IsControlledBy(s, a) {
		return "", fmt.Errorf("secret %s is not controlled by this Agent", s.Name)
	}
	return string(s.Data[keyField]), nil
}

func (r *AgentReconciler) provision(ctx context.Context, a *corev1alpha1.Agent) error {
	log := logf.FromContext(ctx)
	if controllerutil.AddFinalizer(a, finalizer) {
		if err := r.Update(ctx, a); err != nil {
			return err
		}
	}
	// The Secret comes first. A retry after a crash finds the key value in it.
	key, err := r.runKey(ctx, a)
	if err != nil {
		return err
	}
	if key == "" {
		if key, err = newKey(); err != nil {
			return err
		}
		if err := r.Create(ctx, r.secret(a, key)); err != nil {
			return err
		}
	}
	alias := "kd-" + string(a.UID)
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
		if apierrors.IsForbidden(err) {
			// The API server refuses Role rules that are not in the ceiling ClusterRole.
			log.Info("The API server refused the run", "kind", fmt.Sprintf("%T", o), "reason", err.Error())
			return r.revoke(ctx, a, corev1alpha1.AgentFailed, "Forbidden")
		}
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	a.Status.Phase = corev1alpha1.AgentRunning
	a.Status.Job = name
	a.Status.KeyAlias = alias
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
	return &corev1.Secret{ObjectMeta: runMeta(a), StringData: map[string]string{
		"ANTHROPIC_BASE_URL": r.GatewayURL, keyField: key, "ANTHROPIC_MODEL": m,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": m, "ANTHROPIC_DEFAULT_SONNET_MODEL": m,
		"ANTHROPIC_DEFAULT_OPUS_MODEL": m, "ANTHROPIC_SMALL_FAST_MODEL": m, "CLAUDE_CODE_SUBAGENT_MODEL": m,
		"OPENAI_BASE_URL": r.GatewayURL + "/v1", "OPENAI_API_KEY": key,
	}}
}

func runJob(a *corev1alpha1.Agent) *batchv1.Job {
	name := runName(a)
	var env []corev1.EnvVar
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

// revoke blocks the key first. Then it deletes the access, reads the usage, writes the status,
// deletes the Secret and removes the finalizer. The Secret goes last, so a retry after a crash
// still has the key value to block the key and to read the usage.
func (r *AgentReconciler) revoke(ctx context.Context, a *corev1alpha1.Agent, phase corev1alpha1.AgentPhase,
	reason string) error {
	key, err := r.runKey(ctx, a)
	if err != nil {
		return err
	}
	if key != "" {
		if err := r.Gateway.BlockKey(ctx, key); err != nil {
			return err
		}
	}
	m := runMeta(a)
	var gone []client.Object
	if !a.DeletionTimestamp.IsZero() {
		// Stop the pods of a run that is still active.
		gone = append(gone, &batchv1.Job{ObjectMeta: m})
	}
	gone = append(gone, &rbacv1.RoleBinding{ObjectMeta: m}, &rbacv1.Role{ObjectMeta: m},
		&corev1.ServiceAccount{ObjectMeta: m})
	for _, o := range gone {
		err := r.Delete(ctx, o, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	if key != "" {
		if err := r.writeUsage(ctx, a, key, phase, reason); err != nil {
			return err
		}
		if err := r.Delete(ctx, &corev1.Secret{ObjectMeta: m}); client.IgnoreNotFound(err) != nil {
			return err
		}
		logf.FromContext(ctx).Info("Revoked the run", "keyAlias", a.Status.KeyAlias, "reason", reason)
	} else if a.Status.RevokedAt == nil {
		// No Secret: the controller did not make a key for this Agent.
		a.Status.Phase = phase
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type: condRevoked, Status: metav1.ConditionTrue, Reason: reason,
			Message: "There was no key to revoke.", ObservedGeneration: a.Generation,
		})
		a.Status.RevokedAt = &metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, a); err != nil {
			return err
		}
	}
	if controllerutil.RemoveFinalizer(a, finalizer) {
		return r.Update(ctx, a)
	}
	return nil
}

func (r *AgentReconciler) writeUsage(ctx context.Context, a *corev1alpha1.Agent, key string,
	phase corev1alpha1.AgentPhase, reason string) error {
	msg := "The key is blocked and the credentials are deleted."
	u, err := r.Gateway.Usage(ctx, key)
	if errors.Is(err, gateway.ErrSpendNotSettled) {
		msg += " The spend did not settle, so the usage can be low."
	} else if err != nil {
		return err
	}
	a.Status.Usage = &corev1alpha1.AgentUsage{
		Spend: strconv.FormatFloat(u.Spend, 'f', -1, 64), Calls: u.Calls, FailedCalls: u.FailedCalls,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
	}
	a.Status.Phase = phase
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
