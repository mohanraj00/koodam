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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	// WorkspaceLabel is on each object that the controller makes for a Workspace.
	WorkspaceLabel = "koodam.dev/workspace"

	// The keys and the paths of the pod contract in issue #15.
	hostKeyField        = "ssh_host_ed25519_key"
	hostPubField        = "ssh_host_ed25519_key.pub"
	authorizedKeysField = "authorized_keys"
	workspaceHome       = "/home/dev"
	workspaceSSHDir     = "/etc/koodam/ssh"
	workspaceGlobalsDir = "/etc/koodam/globals"
	workspaceLoginDir   = "/etc/koodam/login"
	globalsConfigMap    = "koodam-globals"
	loginSecret         = "koodam-login"
	workspaceUser       = int64(1000)
	workspacePort       = int32(2222)

	// maxWorkspaceName keeps the pod label controller-revision-hash (name, "-", 10 characters)
	// at 63 characters or fewer.
	maxWorkspaceName = 52

	condReady = "Ready"
	// condKeysValid is False if the controller skipped a key of spec.sshPublicKeys.
	condKeysValid = "SSHKeysValid"
	// condProfileBound is False if no Profile binds the namespace of the Workspace.
	condProfileBound  = "ProfileBound"
	reasonNotBound    = "NamespaceNotBound"
	reasonNamespaceOK = "NamespaceBound"

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

// WorkspaceReconciler makes the host key Secret, the authorized_keys ConfigMap, the gateway key
// Secret and the StatefulSet of a Workspace. The owner references delete them when the Workspace
// is deleted. The finalizer keeps the Workspace until the gateway key is blocked.
type WorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the host key Secret. The controller has no list or watch on Secrets.
	APIReader client.Reader
	// Image is the workspace image if spec.image is empty.
	Image string
	// InstallerImage has /harness-install. The pod uses it as an init container for each
	// harness of spec.harnesses.
	InstallerImage string
	// Gateway makes, renews and blocks the gateway keys.
	Gateway gateway.Gateway
	// GatewayURL is the gateway address for the workspace pods.
	GatewayURL string
	// Egress sends the egress of the workspace pods through the egress proxy.
	Egress EgressProxy
	// KeyTTL is the TTL of a gateway key. If it is 0, the controller uses DefaultWorkspaceKeyTTL.
	// The controller renews a key when the last renew is older than KeyTTL/4.
	KeyTTL time.Duration
	// Now gives the time for the renew. If it is nil, the controller uses time.Now.
	Now func() time.Time

	renewMu sync.Mutex
	// renewed has the time of the last renew for each key hash.
	renewed map[string]time.Time
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=workspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;delete

// Reconcile makes the objects of the Workspace and writes its status.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ws := &corev1alpha1.Workspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ws.DeletionTimestamp.IsZero() {
		// Block the key first. Then the garbage collector deletes the owned objects. The home PVC stays.
		return ctrl.Result{}, r.applyKey(ctx, ws)
	}
	old := ws.Status.DeepCopy()
	user, err := boundProfile(ctx, r.Client, ws.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	bound := user != ""
	phase, reason, msg, err := r.apply(ctx, ws, user)
	if err != nil {
		return ctrl.Result{}, err
	}
	ws.Status.Phase = phase
	ws.Status.Pod = ""
	if phase != corev1alpha1.WorkspaceFailed && bound {
		ws.Status.Pod = ws.Name + "-0"
	}
	// The message names the namespace and the label only.
	boundCond := metav1.Condition{Type: condProfileBound, Status: metav1.ConditionTrue, Reason: reasonNamespaceOK,
		Message: "A Profile binds the namespace.", ObservedGeneration: ws.Generation}
	if !bound {
		boundCond.Status, boundCond.Reason, boundCond.Message = metav1.ConditionFalse, reasonNotBound, msg
	}
	meta.SetStatusCondition(&ws.Status.Conditions, boundCond)
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
	meta.SetStatusCondition(&ws.Status.Conditions, harnessCondition(ws, r.InstallerImage))
	meta.SetStatusCondition(&ws.Status.Conditions, r.Egress.condition(ws.Generation))
	res := ctrl.Result{}
	var keyErr error
	if c := meta.FindStatusCondition(ws.Status.Conditions, condGatewayKey); bound && wantKey(ws) && c != nil &&
		c.Reason == reasonRenewFailed {
		keyErr = errRenewFailed
	} else if bound && wantKey(ws) && ws.Status.KeyHash != "" {
		res.RequeueAfter = max(time.Second, r.untilRenew(ws.Status.KeyHash))
	}
	if old.Phase == ws.Status.Phase && old.Pod == ws.Status.Pod && old.HostKey == ws.Status.HostKey &&
		conditionsEqual(old.Conditions, ws.Status.Conditions) {
		return res, keyErr
	}
	if err := r.Status().Update(ctx, ws); err != nil {
		return res, err
	}
	return res, keyErr
}

// apply makes or updates the 3 objects of the Workspace. It returns the phase, and the reason
// and the message of the Ready condition. It returns an error only if a retry can fix it. If
// user (the bound Profile) is empty, apply makes no object and stops the pod.
func (r *WorkspaceReconciler) apply(ctx context.Context, ws *corev1alpha1.Workspace, user string) (
	corev1alpha1.WorkspacePhase, string, string, error) {
	const failed = corev1alpha1.WorkspaceFailed
	if len(ws.Name) > maxWorkspaceName {
		return failed, "Invalid", "The Workspace name is too long for the StatefulSet. Use 52 characters or fewer.", nil
	}
	if user == "" {
		if err := r.unbind(ctx, ws); err != nil {
			return "", "", "", err
		}
		return corev1alpha1.WorkspacePending, reasonNotBound, fmt.Sprintf("No Profile binds the namespace %q. "+
			"Add the label %s=<profile> to the namespace, and add the namespace to spec.namespaces of that Profile.",
			ws.Namespace, corev1alpha1.ProfileLabel), nil
	}
	// The template check comes before the key. A template that is not valid makes no key and no
	// pod. The controller does not change a StatefulSet that exists.
	want, err := r.mergedStatefulSet(ctx, ws, user)
	if err != nil && !isInvalidTemplate(err) {
		return "", "", "", err
	}
	if err != nil {
		return failed, reasonInvalidPodTemplate, err.Error(), nil
	}
	pub, err := r.hostKey(ctx, ws)
	if err == nil {
		ws.Status.HostKey = pub
		err = r.applyConfigMap(ctx, ws)
	}
	var sts *appsv1.StatefulSet
	if err == nil {
		// The key comes before the StatefulSet: the pod mounts the Secret, and a suspend
		// blocks the key before the pod stops.
		err = r.applyKey(ctx, ws)
	}
	if err == nil {
		sts, err = r.applyStatefulSet(ctx, ws, want)
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
	case sts.Status.ObservedGeneration >= sts.Generation && sts.Status.ReadyReplicas > 0:
		// The ready replica must come from the current StatefulSet spec.
		return corev1alpha1.WorkspaceRunning, "PodReady", "The pod is ready for SSH.", nil
	default:
		return corev1alpha1.WorkspacePending, "PodNotReady", "The pod is not ready.", nil
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

// unbind stops the Workspace in a namespace that no Profile binds. It uses the order of a
// suspend: it blocks the gateway key first, then it scales the StatefulSet to 0. It makes no
// object. The home PVC stays.
func (r *WorkspaceReconciler) unbind(ctx context.Context, ws *corev1alpha1.Workspace) error {
	secret, err := r.readKey(ctx, ws)
	if err != nil && !errors.Is(err, errNotOwned) {
		return err
	}
	if err := r.revokeKey(ctx, ws, secret); err != nil {
		return err
	}
	sts := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: ws.Name}, sts)
	if apierrors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(sts, ws)) {
		return nil
	}
	if err != nil {
		return err
	}
	want, err := r.mergedStatefulSet(ctx, ws, "")
	if err != nil {
		// The StatefulSet has 0 replicas, so the template makes no pod.
		want = r.statefulSet(ws, "")
	}
	_, err = r.applyStatefulSet(ctx, ws, want)
	return err
}

// applyStatefulSet makes the StatefulSet. It updates the pod template only if it is not the
// template of the controller, and the replicas if spec.suspend or the binding changed. The
// volumeClaimTemplates cannot change.
func (r *WorkspaceReconciler) applyStatefulSet(ctx context.Context, ws *corev1alpha1.Workspace,
	want *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	have := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: ws.Name}, have)
	if apierrors.IsNotFound(err) {
		return want, r.createOwned(ctx, ws, want)
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(have, ws) {
		return nil, errNotOwned
	}
	// DeepDerivative ignores the fields that the API server sets as defaults. Thus the
	// controller updates the template only if it changed, for example after a manual edit.
	// It also ignores an empty field in want, and accepts a list in want that is the start of the
	// list in have. The egress proxy owns hostAliases and dnsConfig, thus the controller compares
	// them exactly (#142).
	w, h := want.Spec.Template.Spec, have.Spec.Template.Spec
	sameTemplate := equality.Semantic.DeepDerivative(want.Spec.Template, have.Spec.Template) &&
		equality.Semantic.DeepEqual(w.HostAliases, h.HostAliases) &&
		equality.Semantic.DeepEqual(w.DNSConfig, h.DNSConfig)
	if sameTemplate && have.Spec.Replicas != nil && *have.Spec.Replicas == *want.Spec.Replicas {
		return have, nil
	}
	if !sameTemplate {
		have.Spec.Template = want.Spec.Template
	}
	have.Spec.Replicas = want.Spec.Replicas
	return have, r.Update(ctx, have)
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

// statefulSet makes the StatefulSet of the pod contract in issue #15. If user (the bound Profile)
// is empty, the StatefulSet has 0 replicas.
func (r *WorkspaceReconciler) statefulSet(ws *corev1alpha1.Workspace, user string) *appsv1.StatefulSet {
	const home, ssh, globals, login = "home", "ssh", "globals", "login"
	image := ws.Spec.Image
	if image == "" {
		image = r.Image
	}
	replicas := int32(1)
	if ws.Spec.Suspend || user == "" {
		replicas = 0
	}
	storage := ws.Spec.Storage
	if storage.IsZero() {
		storage = resource.MustParse("20Gi")
	}
	labels := map[string]string{WorkspaceLabel: ws.Name}
	volumes := []corev1.Volume{
		// One projected volume gives the 2 files of the contract. The kubelet updates the files.
		// The secret files are owner root, group fsGroup 1000, mode 0440. Thus sshd (uid 1000)
		// reads the host key, and sshd accepts the mode because the owner is not uid 1000.
		{Name: ssh, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{
				{ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: sshConfigMapName(ws)},
					Items:                []corev1.KeyToPath{{Key: authorizedKeysField, Path: authorizedKeysField}}}},
				{Secret: &corev1.SecretProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: hostKeySecretName(ws)},
					Items: []corev1.KeyToPath{{Key: hostKeyField, Path: "host/" + hostKeyField,
						Mode: new(int32(0o440))}}}},
			}}}},
		{Name: globals, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: globalsConfigMap}, Optional: new(true)}}},
		{Name: login, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: loginSecret, Optional: new(true), DefaultMode: new(int32(0o440))}}},
	}
	container := corev1.Container{
		Name: workspaceContainer, Image: image,
		Ports: []corev1.ContainerPort{{Name: "ssh", ContainerPort: workspacePort, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(workspacePort)}}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: home, MountPath: workspaceHome},
			{Name: ssh, MountPath: workspaceSSHDir, ReadOnly: true},
			{Name: globals, MountPath: workspaceGlobalsDir, ReadOnly: true},
			{Name: login, MountPath: workspaceLoginDir, ReadOnly: true},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(workspaceUser), RunAsGroup: new(workspaceUser),
			AllowPrivilegeEscalation: new(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
	if ws.Spec.Gateway != nil {
		// The gateway key is a file, not an env: sshd does not give its env to the SSH sessions.
		// The image exports the files in a login shell. The kubelet updates the files after a
		// key change, so a new login gets the new key.
		const gw = "gateway"
		volumes = append(volumes, corev1.Volume{Name: gw, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: gatewaySecretName(ws), DefaultMode: new(int32(0o440))}}})
		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{Name: gw, MountPath: workspaceGatewayDir, ReadOnly: true})
	}
	tmpl := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: new(false),
		EnableServiceLinks:           new(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(workspaceUser), RunAsGroup: new(workspaceUser),
			FSGroup:        new(workspaceUser),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{container},
		Volumes:    volumes,
	}}
	addWorkspaceHarnesses(&tmpl.Spec, ws.Spec.Harnesses, r.InstallerImage)
	addIdentity(&tmpl.ObjectMeta, "", user)
	r.Egress.annotate(&tmpl.ObjectMeta, ws.Spec.Gateway != nil, r.GatewayURL)
	sts := &appsv1.StatefulSet{ObjectMeta: workspaceMeta(ws, ws.Name), Spec: appsv1.StatefulSetSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: tmpl,
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: home, Labels: labels},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: storage}},
			},
		}},
		// The home PVC stays after a scale to 0 and after a Workspace delete.
		PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		},
	}}
	return sts
}

// workspacesIn returns a request for each Workspace in the namespace.
func (r *WorkspaceReconciler) workspacesIn(ctx context.Context, namespace string) []reconcile.Request {
	list := &corev1alpha1.WorkspaceList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the Workspaces", "namespace", namespace)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, ws := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ws)})
	}
	return reqs
}

// workspacesForNamespace maps a Namespace to its Workspaces. Thus a label change reconciles them.
func (r *WorkspaceReconciler) workspacesForNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.workspacesIn(ctx, obj.GetName())
}

// workspacesForProfile maps a Profile to the Workspaces in the namespaces that it lists. On an
// update, the handler calls it for the old and the new Profile. Thus a removed namespace is also
// reconciled.
func (r *WorkspaceReconciler) workspacesForProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	p, ok := obj.(*corev1alpha1.Profile)
	if !ok {
		return nil
	}
	var reqs []reconcile.Request
	for _, ns := range p.Spec.Namespaces {
		reqs = append(reqs, r.workspacesIn(ctx, ns)...)
	}
	return reqs
}

// deleteStarted is true if the update gives the object a deletionTimestamp.
func deleteStarted(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return false
	}
	return e.ObjectOld.GetDeletionTimestamp().IsZero() && !e.ObjectNew.GetDeletionTimestamp().IsZero()
}

// SetupWithManager sets up the controller with the Manager. It does not watch the Secrets,
// because the controller has no list or watch on Secrets. It watches the Namespaces and the
// Profiles, because they give the binding.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Workspace{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.workspacesForNamespace)).
		// A status write of a Profile does not change the binding. Thus only a spec change or the
		// start of a delete reconciles the Workspaces. A create and a delete always pass.
		Watches(&corev1alpha1.Profile{}, handler.EnqueueRequestsFromMapFunc(r.workspacesForProfile),
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{},
				predicate.Funcs{UpdateFunc: deleteStarted}))).
		Named("workspace").
		Complete(r)
}
