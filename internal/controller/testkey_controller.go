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
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/podtemplate"
)

const (
	testKeyFinalizer = "core.koodam.dev/testkey-revoke"
	condActive       = "Active"

	// The fields of the slot Secret. A later change mounts them in the Workspace pod (#192).
	testKeyValueField = "key"
	testKeyAliasField = "alias"
	testKeyURLField   = "base-url"
	testKeyModelField = "model"

	// DefaultTestKeyRevokeWait is the LiteLLM auth cache TTL (60s) plus a margin. It is a
	// second limit after the block. The fence is the single-instance gateway (#192).
	DefaultTestKeyRevokeWait = 75 * time.Second

	// testKeyAliasIDMax is the maximum count of test ID characters in the key alias.
	testKeyAliasIDMax = 48
)

// The reasons of the condition Active.
const (
	reasonIssued        = "Issued"
	reasonTKRevoking    = "Revoking"
	reasonTKBlocked     = "Blocked"
	reasonTKRevoked     = "Revoked"
	reasonSlotBusy      = "SlotBusy"
	reasonShape         = "GatewayShapeUnsupported"
	reasonNoWorkspace   = "WorkspaceNotFound"
	reasonNoGateway     = "GatewayDisabled"
	reasonUnbound       = "ProfileNotBound"
	reasonGatewayFailed = "GatewayError"
)

// TestKeyReconciler manages gateway keys for individual Workspace tests.
//
// The fence of a revoke is the block response of a gateway with one replica and one worker.
// The runtime proof of #192 shows that a block does not reach other replicas or workers
// before the auth cache TTL. Thus the controller issues keys only if SingleInstanceGateway
// is true.
type TestKeyReconciler struct {
	client.Client
	APIReader client.Reader
	Gateway   gateway.Gateway
	// GatewayURL is the gateway address for the Workspace pods.
	GatewayURL string
	// SingleInstanceGateway is true only if the gateway has one replica and one worker.
	SingleInstanceGateway bool
	// RevokeWait is the time after the block before the spend read and the cleanup.
	RevokeWait time.Duration
	// Now is the clock. If it is nil, the controller uses time.Now.
	Now func() time.Time
}

// testKeySecretName is the name of the slot Secret of a Workspace. The prefix is reserved, so a
// pod template cannot name the slot. The prefix is not the "kd-" of the Agent run Secrets, and
// the suffix is not the "-gateway" or "-ssh-host" of the Workspace Secrets. Thus no other
// Secret of Koodam has a slot name.
func testKeySecretName(workspace string) string {
	return podtemplate.TestKeySecretPrefix + workspace + "-testkey"
}

// testKeyAlias makes a bounded alias from the test ID and the TestKey UID.
func testKeyAlias(key *corev1alpha1.TestKey) string {
	id := key.Spec.TestID
	if len(id) > testKeyAliasIDMax {
		id = id[:testKeyAliasIDMax]
	}
	uid := string(key.UID)
	if len(uid) > 8 {
		uid = uid[:8]
	}
	return "tk-" + id + "-" + uid
}

func (r *TestKeyReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *TestKeyReconciler) revokeWait() time.Duration {
	if r.RevokeWait > 0 {
		return r.RevokeWait
	}
	return DefaultTestKeyRevokeWait
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=testkeys,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=testkeys/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=testkeys/finalizers,verbs=update

// Reconcile issues the key of a TestKey, and revokes it at the end. The key value goes only
// to the slot Secret: never to a log, an event, the status or an error.
func (r *TestKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := &corev1alpha1.TestKey{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, key); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if key.Status.RevokedAt != nil {
		return ctrl.Result{}, r.dropFinalizer(ctx, key)
	}
	workspace, err := r.readWorkspace(ctx, key)
	if err != nil {
		return ctrl.Result{}, err
	}
	reason, message := r.refusal(key, workspace)
	c := meta.FindStatusCondition(key.Status.Conditions, condActive)
	ending := c != nil && (c.Reason == reasonTKRevoking || c.Reason == reasonTKBlocked)
	expired := key.Status.ExpiresAt != nil && !r.now().Before(key.Status.ExpiresAt.Time)
	if !ending && !expired && reason == "" && key.DeletionTimestamp.IsZero() {
		return r.issue(ctx, key, workspace)
	}
	if key.Status.KeyHash != "" {
		return r.revoke(ctx, key)
	}
	// No gateway key exists, because the hash goes to the status before CreateKey.
	if err := r.deleteSlot(ctx, key); err != nil {
		return ctrl.Result{}, err
	}
	if !key.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.dropFinalizer(ctx, key)
	}
	return ctrl.Result{}, r.setActive(ctx, key, metav1.ConditionFalse, reason, message)
}

// readWorkspace reads the Workspace of key without the cache. It returns nil if the Workspace
// does not exist.
func (r *TestKeyReconciler) readWorkspace(ctx context.Context, key *corev1alpha1.TestKey) (
	*corev1alpha1.Workspace, error) {
	workspace := &corev1alpha1.Workspace{}
	ref := client.ObjectKey{Namespace: key.Namespace, Name: key.Spec.WorkspaceRef.Name}
	if err := r.APIReader.Get(ctx, ref, workspace); apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return workspace, nil
}

// refusal returns a reason if the TestKey must not have a key now.
func (r *TestKeyReconciler) refusal(key *corev1alpha1.TestKey, workspace *corev1alpha1.Workspace) (string, string) {
	switch {
	case workspace == nil:
		return reasonNoWorkspace, "The Workspace does not exist."
	case !r.SingleInstanceGateway:
		return reasonShape, "Test keys need a gateway with one replica and one worker."
	case workspace.Spec.Gateway == nil:
		return reasonNoGateway, "The Workspace has no gateway."
	}
	if reason, message := testKeyRefusal(key, workspace); reason != "" {
		return reason, message
	}
	// The Workspace controller blocks the Workspace key when no Profile binds the namespace.
	// The test keys stop with it. A new Workspace has no condition yet, so it gets no key.
	if !meta.IsStatusConditionTrue(workspace.Status.Conditions, condProfileBound) {
		return reasonUnbound, "No Profile binds the namespace of the Workspace."
	}
	return "", ""
}

func testKeyRefusal(key *corev1alpha1.TestKey, workspace *corev1alpha1.Workspace) (reason, message string) {
	policy := workspace.Spec.TestKeys
	switch {
	case workspace.UID != key.Spec.WorkspaceRef.UID:
		return "WorkspaceUIDMismatch", "The Workspace UID differs from the reference."
	case workspace.Spec.Suspend || !workspace.DeletionTimestamp.IsZero():
		return "WorkspaceSuspended", "The Workspace is suspended or is being deleted."
	case policy == nil:
		return "TestKeysDisabled", "The Workspace does not permit test keys."
	case key.Spec.Budget.Cmp(policy.MaxBudget) > 0:
		return "BudgetLimitExceeded", "The test key budget exceeds the Workspace limit."
	case key.Spec.TTL.Duration > policy.MaxTTL.Duration:
		return "TTLLimitExceeded", "The test key TTL exceeds the Workspace limit."
	}
	return "", ""
}

// issue follows the approved create order: the finalizer, the slot, the status hash, the
// gateway key, then Active. A retry uses the key in the slot and the remaining TTL.
func (r *TestKeyReconciler) issue(ctx context.Context, key *corev1alpha1.TestKey,
	workspace *corev1alpha1.Workspace) (ctrl.Result, error) {
	if controllerutil.AddFinalizer(key, testKeyFinalizer) {
		if err := r.updateMeta(ctx, key); err != nil {
			return ctrl.Result{}, err
		}
	}
	slot, err := r.readSlot(ctx, key)
	if err != nil {
		return ctrl.Result{}, err
	}
	if slot == nil && key.Status.KeyHash != "" {
		// The slot of the recorded key is gone. Do not make a second key for one TestKey.
		return r.revoke(ctx, key)
	}
	if slot == nil {
		if slot, err = r.createSlot(ctx, key, workspace); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !metav1.IsControlledBy(slot, key) {
		err := r.setActive(ctx, key, metav1.ConditionFalse, reasonSlotBusy,
			"Another test key of this Workspace is active.")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}
	value := string(slot.Data[testKeyValueField])
	if key.Status.KeyHash == "" {
		key.Status.KeyHash = gateway.HashKey(value)
		key.Status.KeyAlias = string(slot.Data[testKeyAliasField])
		key.Status.ExpiresAt = &metav1.Time{Time: r.now().Add(key.Spec.TTL.Duration).Truncate(time.Second)}
		if err := r.Status().Update(ctx, key); err != nil {
			return ctrl.Result{}, err
		}
	}
	remaining := key.Status.ExpiresAt.Sub(r.now()).Truncate(time.Second)
	if gateway.HashKey(value) != key.Status.KeyHash || remaining < time.Second {
		return r.revoke(ctx, key)
	}
	// A suspend, an unbind or a delete can start after the first Workspace read. From the hash
	// write on, the Workspace controller sees this key and waits for its block. A stop that did
	// not see the hash came before the hash write, so this second read sees the stop (#192).
	if workspace, err = r.readWorkspace(ctx, key); err != nil {
		return ctrl.Result{}, err
	}
	if reason, _ := r.refusal(key, workspace); reason != "" {
		return r.revoke(ctx, key)
	}
	err = r.Gateway.CreateKey(ctx, gateway.KeySpec{
		Value: value, Alias: key.Status.KeyAlias, Model: workspace.Spec.Gateway.Model,
		Budget: key.Spec.Budget.AsApproximateFloat64(), TTL: remaining,
		Metadata: map[string]string{"koodam_test_id": key.Spec.TestID, "koodam_testkey": key.Name},
	})
	if err != nil {
		if e := r.setActive(ctx, key, metav1.ConditionFalse, reasonGatewayFailed,
			"The gateway did not make the test key."); e != nil {
			return ctrl.Result{}, e
		}
		return ctrl.Result{}, err
	}
	err = r.setActive(ctx, key, metav1.ConditionTrue, reasonIssued, "The gateway key is active.")
	return ctrl.Result{RequeueAfter: remaining}, err
}

// createSlot makes the slot Secret with a new key value. The create is atomic, so a second
// TestKey of the Workspace gets AlreadyExists, reads the Secret of the first and waits. The
// Secret is not immutable: the kubelet stops the watch of an immutable Secret, so a running pod
// keeps the files of a deleted slot. The status hash finds a changed value, and a changed value
// gets a revoke.
func (r *TestKeyReconciler) createSlot(ctx context.Context, key *corev1alpha1.TestKey,
	workspace *corev1alpha1.Workspace) (*corev1.Secret, error) {
	value, err := gateway.NewKeyValue()
	if err != nil {
		return nil, err
	}
	slot := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testKeySecretName(workspace.Name), Namespace: key.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "koodam"}},
		StringData: map[string]string{testKeyValueField: value, testKeyAliasField: testKeyAlias(key),
			testKeyURLField: r.GatewayURL, testKeyModelField: workspace.Spec.Gateway.Model},
	}
	if err := controllerutil.SetControllerReference(key, slot, r.Scheme()); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, slot); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	slot, err = r.readSlot(ctx, key)
	if err == nil && slot == nil {
		err = errors.New("the slot Secret is missing after its create")
	}
	return slot, err
}

func (r *TestKeyReconciler) readSlot(ctx context.Context, key *corev1alpha1.TestKey) (*corev1.Secret, error) {
	slot := &corev1.Secret{}
	name := types.NamespacedName{Namespace: key.Namespace, Name: testKeySecretName(key.Spec.WorkspaceRef.Name)}
	if err := r.APIReader.Get(ctx, name, slot); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return slot, nil
}

// revoke follows the approved end order: Revoking, the block, the wait, the spend read, the
// slot delete, Revoked, then the finalizer. Each step can run again. The slot stays until the
// spend read shows a blocked row, or until a late key is past its expiry.
func (r *TestKeyReconciler) revoke(ctx context.Context, key *corev1alpha1.TestKey) (ctrl.Result, error) {
	c := meta.FindStatusCondition(key.Status.Conditions, condActive)
	if c == nil || (c.Reason != reasonTKRevoking && c.Reason != reasonTKBlocked) {
		if err := r.setActive(ctx, key, metav1.ConditionFalse, reasonTKRevoking,
			"The revoke of the test key started."); err != nil {
			return ctrl.Result{}, err
		}
		c = meta.FindStatusCondition(key.Status.Conditions, condActive)
	}
	if c.Reason == reasonTKRevoking {
		if err := r.Gateway.BlockKey(ctx, key.Status.KeyHash); err != nil {
			return ctrl.Result{}, err
		}
		err := r.setActive(ctx, key, metav1.ConditionFalse, reasonTKBlocked, "The gateway key is blocked.")
		return ctrl.Result{RequeueAfter: r.revokeWait()}, err
	}
	if wait := c.LastTransitionTime.Add(r.revokeWait()).Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	message := "The gateway key is blocked and its Secret is deleted."
	u, err := r.Gateway.Usage(ctx, key.Status.KeyHash)
	switch {
	case errors.Is(err, gateway.ErrKeyNotFound):
		// A block of a missing row does not stop a late create (#192). Block again until a
		// late key is past its expiry, and keep the slot until then.
		if r.now().Before(key.Status.ExpiresAt.Add(r.revokeWait())) {
			return r.blockAgain(ctx, key, "The gateway had no key for this test. The block runs again.")
		}
		message = "The gateway had no key for this test, and the key time is past."
	case (errors.Is(err, gateway.ErrSpendNotSettled) || err == nil) && !u.Blocked:
		// A late create made a row after the block. Block it and wait again (#192).
		return r.blockAgain(ctx, key, "The gateway key was not blocked. The block runs again.")
	case errors.Is(err, gateway.ErrSpendNotSettled), err == nil:
		if err != nil {
			message += spendNotSettledNote
		}
		key.Status.Usage = &corev1alpha1.AgentUsage{Spend: strconv.FormatFloat(u.Spend, 'f', -1, 64),
			Calls: u.Calls, FailedCalls: u.FailedCalls, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
	default:
		return ctrl.Result{}, err
	}
	if err := r.deleteSlot(ctx, key); err != nil {
		return ctrl.Result{}, err
	}
	logf.FromContext(ctx).Info("Revoked the test key", "keyAlias", key.Status.KeyAlias)
	key.Status.RevokedAt = &metav1.Time{Time: r.now()}
	meta.RemoveStatusCondition(&key.Status.Conditions, condActive)
	meta.SetStatusCondition(&key.Status.Conditions, metav1.Condition{Type: condActive,
		Status: metav1.ConditionFalse, Reason: reasonTKRevoked, Message: message,
		ObservedGeneration: key.Generation, LastTransitionTime: metav1.NewTime(r.now())})
	if err := r.Status().Update(ctx, key); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.dropFinalizer(ctx, key)
}

// blockAgain blocks the key and starts the revoke wait again. The wait counts from this block,
// so a key row that a late create made gets the full wait.
func (r *TestKeyReconciler) blockAgain(ctx context.Context, key *corev1alpha1.TestKey,
	message string) (ctrl.Result, error) {
	if err := r.Gateway.BlockKey(ctx, key.Status.KeyHash); err != nil {
		return ctrl.Result{}, err
	}
	meta.RemoveStatusCondition(&key.Status.Conditions, condActive)
	meta.SetStatusCondition(&key.Status.Conditions, metav1.Condition{Type: condActive,
		Status: metav1.ConditionFalse, Reason: reasonTKBlocked, Message: message,
		ObservedGeneration: key.Generation, LastTransitionTime: metav1.NewTime(r.now())})
	return ctrl.Result{RequeueAfter: r.revokeWait()}, r.Status().Update(ctx, key)
}

// deleteSlot deletes the slot Secret only if it is from key. The UID precondition stops the
// delete if the Secret changed after the read.
func (r *TestKeyReconciler) deleteSlot(ctx context.Context, key *corev1alpha1.TestKey) error {
	slot, err := r.readSlot(ctx, key)
	if err != nil || slot == nil || !metav1.IsControlledBy(slot, key) {
		return err
	}
	uid := slot.UID
	return client.IgnoreNotFound(r.Delete(ctx, slot, client.Preconditions{UID: &uid}))
}

// setActive writes the condition Active if it changed. A new reason is a new step, so it gets
// a new transition time. The revoke wait counts from the time of the Blocked step.
func (r *TestKeyReconciler) setActive(ctx context.Context, key *corev1alpha1.TestKey,
	status metav1.ConditionStatus, reason, message string) error {
	old := meta.FindStatusCondition(key.Status.Conditions, condActive)
	if old != nil && old.Status == status && old.Reason == reason && old.Message == message &&
		old.ObservedGeneration == key.Generation {
		return nil
	}
	if old != nil && old.Reason != reason {
		meta.RemoveStatusCondition(&key.Status.Conditions, condActive)
	}
	meta.SetStatusCondition(&key.Status.Conditions, metav1.Condition{Type: condActive, Status: status,
		Reason: reason, Message: message, ObservedGeneration: key.Generation,
		LastTransitionTime: metav1.NewTime(r.now())})
	return r.Status().Update(ctx, key)
}

func (r *TestKeyReconciler) dropFinalizer(ctx context.Context, key *corev1alpha1.TestKey) error {
	if controllerutil.RemoveFinalizer(key, testKeyFinalizer) {
		return r.updateMeta(ctx, key)
	}
	return nil
}

// updateMeta writes the metadata and keeps the status in memory.
func (r *TestKeyReconciler) updateMeta(ctx context.Context, key *corev1alpha1.TestKey) error {
	st := key.Status.DeepCopy()
	err := r.Update(ctx, key)
	key.Status = *st
	return err
}

// testKeysFor maps a Workspace event to its TestKeys, so a suspend or a delete starts the revoke.
func (r *TestKeyReconciler) testKeysFor(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &corev1alpha1.TestKeyList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.WorkspaceRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// SetupWithManager registers the controller. It does not watch Secrets, because the controller
// has no list or watch grant on Secrets.
func (r *TestKeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.TestKey{}).
		Watches(&corev1alpha1.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.testKeysFor)).
		Named("testkey").
		Complete(r)
}
