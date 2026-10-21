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
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// WorkspaceLabel is on each object that the controller makes for a Workspace.
	WorkspaceLabel = "koodam.dev/workspace"

	// The keys of the pod contract in issue #15.
	hostKeyField        = "ssh_host_ed25519_key"
	hostPubField        = "ssh_host_ed25519_key.pub"
	authorizedKeysField = "authorized_keys"

	condReady = "Ready"
	// condKeysValid is False if the controller skipped a key of spec.sshPublicKeys.
	condKeysValid = "SSHKeysValid"

	// maxKeyLength is the maximum length of a key line. It is the same as in the CRD.
	maxKeyLength = 8192
)

// keyLine is the rule of the CRD for a key line. The controller checks the keys again, because
// an old CRD or a bypass of the API validation can give a line with an option such as command=.
var keyLine = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|` +
	`sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com|ssh-rsa) [A-Za-z0-9+/]+={0,2}( [^\r\n]*)?$`)

// validKeys returns the keys that obey keyLine and the indexes of the keys that do not.
func validKeys(keys []string) (valid []string, skipped []int) {
	for i, k := range keys {
		if len(k) > maxKeyLength || !keyLine.MatchString(k) {
			skipped = append(skipped, i)
			continue
		}
		valid = append(valid, k)
	}
	return valid, skipped
}

// WorkspaceReconciler makes the host key Secret and the authorized_keys ConfigMap of a
// Workspace. The owner references delete them when the Workspace is deleted.
type WorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the host key Secret. The controller has no list or watch on Secrets.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create

// Reconcile makes the objects of the Workspace and writes its status.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ws := &corev1alpha1.Workspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ws.DeletionTimestamp.IsZero() {
		// The garbage collector deletes the owned objects. The home PVC stays.
		return ctrl.Result{}, nil
	}
	old := ws.Status.DeepCopy()
	phase, reason, msg, err := r.apply(ctx, ws)
	if err != nil {
		return ctrl.Result{}, err
	}
	ws.Status.Phase = phase
	ready := metav1.ConditionFalse
	if phase == corev1alpha1.WorkspaceRunning {
		ready = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{
		Type: condReady, Status: ready, Reason: reason, Message: msg, ObservedGeneration: ws.Generation,
	})
	// The message names the indexes only. It never has the key text.
	keysCond := metav1.Condition{Type: condKeysValid, Status: metav1.ConditionTrue, Reason: "Valid",
		Message: "All the keys are valid.", ObservedGeneration: ws.Generation}
	if _, skipped := validKeys(ws.Spec.SSHPublicKeys); len(skipped) > 0 {
		keysCond.Status, keysCond.Reason = metav1.ConditionFalse, "InvalidKey"
		keysCond.Message = fmt.Sprintf("The controller skipped the keys at the indexes %v of spec.sshPublicKeys.", skipped)
	}
	meta.SetStatusCondition(&ws.Status.Conditions, keysCond)
	if old.Phase == ws.Status.Phase && old.Pod == ws.Status.Pod && old.HostKey == ws.Status.HostKey &&
		conditionsEqual(old.Conditions, ws.Status.Conditions) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, ws)
}

// apply makes or updates the objects of the Workspace. It returns the phase, and the reason and
// the message of the Ready condition. It returns an error only if a retry can fix it.
func (r *WorkspaceReconciler) apply(ctx context.Context, ws *corev1alpha1.Workspace) (
	corev1alpha1.WorkspacePhase, string, string, error) {
	const failed = corev1alpha1.WorkspaceFailed
	pub, err := r.hostKey(ctx, ws)
	if err == nil {
		ws.Status.HostKey = pub
		err = r.applyConfigMap(ctx, ws)
	}
	switch {
	case errors.Is(err, errNotOwned):
		return failed, "NameConflict", "An object with a Workspace object name is not from this Workspace.", nil
	case permanent(err):
		logf.FromContext(ctx).Info("The API server refused a Workspace object", "reason", err.Error())
		return failed, "Refused", "The API server refused a Workspace object.", nil
	case err != nil:
		return "", "", "", err
	case ws.Spec.Suspend:
		return corev1alpha1.WorkspaceSuspended, "Suspended", "The pod is stopped. The home PVC stays.", nil
	default:
		// A later change makes the pod. Until then, the Workspace stays Pending.
		return corev1alpha1.WorkspacePending, "NoPod", "The controller does not make the pod yet.", nil
	}
}

func workspaceMeta(ws *corev1alpha1.Workspace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name, Namespace: ws.Namespace,
		Labels: map[string]string{WorkspaceLabel: ws.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(ws,
			corev1alpha1.GroupVersion.WithKind("Workspace"))},
	}
}

func hostKeySecretName(ws *corev1alpha1.Workspace) string { return ws.Name + "-ssh-host" }
func sshConfigMapName(ws *corev1alpha1.Workspace) string  { return ws.Name + "-ssh" }

// hostKey makes the host key Secret one time only. It returns the public host key. The private
// key goes only to the Secret: never to a log, an event, the status or an error.
func (r *WorkspaceReconciler) hostKey(ctx context.Context, ws *corev1alpha1.Workspace) (string, error) {
	s := &corev1.Secret{}
	name := hostKeySecretName(ws)
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: name}, s)
	if apierrors.IsNotFound(err) {
		private, public, err := newHostKey()
		if err != nil {
			return "", err
		}
		s = &corev1.Secret{ObjectMeta: workspaceMeta(ws, name), Immutable: new(true),
			Data: map[string][]byte{hostKeyField: private, hostPubField: []byte(public)}}
		if err := r.Create(ctx, s); err != nil {
			return "", err
		}
		logf.FromContext(ctx).Info("Made the SSH host key", "secret", name)
		return public, nil
	}
	if err != nil {
		return "", err
	}
	if !metav1.IsControlledBy(s, ws) || len(s.Data[hostPubField]) == 0 {
		return "", errNotOwned
	}
	return string(s.Data[hostPubField]), nil
}

// applyConfigMap writes the valid keys of spec.sshPublicKeys to the ConfigMap. The kubelet
// updates the mounted file, so a key change does not restart the pod.
func (r *WorkspaceReconciler) applyConfigMap(ctx context.Context, ws *corev1alpha1.Workspace) error {
	keys := ""
	if valid, _ := validKeys(ws.Spec.SSHPublicKeys); len(valid) > 0 {
		keys = strings.Join(valid, "\n") + "\n"
	}
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: sshConfigMapName(ws)}, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{ObjectMeta: workspaceMeta(ws, sshConfigMapName(ws)),
			Data: map[string]string{authorizedKeysField: keys}}
		return r.createOwned(ctx, ws, cm)
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(cm, ws) {
		return errNotOwned
	}
	if v, ok := cm.Data[authorizedKeysField]; ok && v == keys {
		return nil
	}
	cm.Data = map[string]string{authorizedKeysField: keys}
	return r.Update(ctx, cm)
}

// createOwned creates o. If an object with the same name exists, the cache did not have it. Then
// createOwned reads it from the API server and returns errNotOwned if it is not from ws.
func (r *WorkspaceReconciler) createOwned(ctx context.Context, ws *corev1alpha1.Workspace, o client.Object) error {
	err := r.Create(ctx, o)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		return err
	}
	if !metav1.IsControlledBy(o, ws) {
		return errNotOwned
	}
	return errors.New("the cache does not have the object yet")
}

// SetupWithManager sets up the controller with the Manager. It does not watch the Secrets,
// because the controller has no list or watch on Secrets.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Workspace{}).
		Owns(&corev1.ConfigMap{}).
		Named("workspace").
		Complete(r)
}
