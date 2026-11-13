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
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

const (
	// appFinalizer keeps the AppVersion until the controller blocks its gateway key.
	appFinalizer = "koodam.dev/app-revoke"
	// appLabel and appVersionLabel are on each object and each pod of an AppVersion. The egress
	// proxy reads them from the pod to find the App and the AppVersion of a call. They must
	// match AppLabel and AppVersionLabel in internal/egress.
	appLabel        = "koodam.dev/app"
	appVersionLabel = "koodam.dev/app-version"
	// appKeyField is the Secret field with the key value. The pod reads it as the file
	// workspaceGatewayDir/key.
	appKeyField = "key"
	// The app container gets the gateway address, the model and the path of the key file in
	// these env variables. No env variable has the key value.
	appGatewayURLEnv = "KOODAM_GATEWAY_URL"
	appModelEnv      = "KOODAM_GATEWAY_MODEL"
	appKeyFileEnv    = "KOODAM_GATEWAY_KEY_FILE"
	// appKeyGroup is the group of the key file in the pod.
	appKeyGroup = int64(1000)
	// reasonIssuing is the reason of the condition GatewayKey from the enrollment until the
	// gateway confirms the key.
	reasonIssuing = "Issuing"
	// appLiveWait is the time between 2 checks of a version that waits for a free live slot.
	appLiveWait = 30 * time.Second
)

// AppVersionReconciler makes a Deployment and a Service for an AppVersion, with one gateway key,
// a scoped Role and traced egress. The status has the key hash, so a restart of the controller
// can revoke a key that it did not issue.
//
// The base role already has get, create and delete on Secrets, ServiceAccounts, Roles and
// RoleBindings. The App controller uses these grants for the key Secret and the version Role, and
// the App role has no marker for them.
type AppVersionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads the AppVersion, the App and the owned objects directly, not from the cache.
	APIReader client.Reader
	Gateway   gateway.Gateway
	// GatewayURL is the gateway address for the app pods.
	GatewayURL string
	// Egress sends the egress of the app pods through the egress proxy.
	Egress EgressProxy
	// KeyTTL is the TTL of a version key. If it is 0, the controller uses DefaultWorkspaceKeyTTL.
	// The controller renews the key when the last renew is older than KeyTTL/4.
	KeyTTL time.Duration
	// Now is the clock. If it is nil, the controller uses time.Now.
	Now func() time.Time

	renewMu sync.Mutex
	// renewed has the time of the last renew for each key hash. The hash is not a credential.
	renewed map[string]time.Time
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=apps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.koodam.dev,resources=appversions,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=appversions/status,verbs=update
// +kubebuilder:rbac:groups=core.koodam.dev,resources=appversions/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;delete

// Reconcile makes the key and the objects of an active AppVersion. It revokes the key when the
// AppVersion is deleted or retired, or when its App or its Secret is gone.
func (r *AppVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.APIReader == nil {
		return ctrl.Result{}, errNoAPIReader
	}
	av := &corev1alpha1.AppVersion{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, av); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch c := meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionRevoking); {
	case c != nil:
		// The revoke is terminal. A retry continues it and does not provision again.
		return ctrl.Result{}, r.revoke(ctx, av, c.Reason)
	case !av.DeletionTimestamp.IsZero():
		return ctrl.Result{}, r.revoke(ctx, av, "Deleted")
	case av.Spec.Lifecycle == corev1alpha1.AppVersionRetiring:
		return ctrl.Result{}, r.revoke(ctx, av, "Retired")
	}
	return r.apply(ctx, av)
}

func appVersionName(av *corev1alpha1.AppVersion) string { return "kd-" + av.Name }

func appVersionLabels(av *corev1alpha1.AppVersion) map[string]string {
	return map[string]string{appLabel: av.Spec.AppRef.Name, appVersionLabel: av.Name}
}

func appVersionMeta(av *corev1alpha1.AppVersion) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: appVersionName(av), Namespace: av.Namespace, Labels: appVersionLabels(av),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(av,
			corev1alpha1.GroupVersion.WithKind("AppVersion"))},
	}
}

// invalidAppVersion returns the reason why the controller cannot make the objects of av, or "".
// The text has field names only, never a value of the template.
func invalidAppVersion(av *corev1alpha1.AppVersion) string {
	// The Service name is a DNS label, and the 2 names are label values.
	if len(appVersionName(av)) > validation.DNS1123LabelMaxLength ||
		len(validation.IsDNS1123Label(appVersionName(av))) > 0 {
		return "The AppVersion name must be a DNS label with 60 characters or fewer."
	}
	if len(validation.IsValidLabelValue(av.Spec.AppRef.Name)) > 0 {
		return "spec.appRef.name must be a label value with 63 characters or fewer."
	}
	if !inCeiling(av.Spec.Template.Rules) {
		return "A rule is not in the agent-ceiling ClusterRole."
	}
	c := appContainer(&av.Spec.Template.PodTemplate.Spec)
	if c == nil || len(c.Ports) == 0 {
		return "The app container must have 1 or more ports."
	}
	for _, p := range c.Ports {
		if p.Name == "" || (p.Protocol != "" && p.Protocol != corev1.ProtocolTCP) || p.HostPort != 0 || p.HostIP != "" {
			return "Each port of the app container must have a name and the TCP protocol, with no host port."
		}
	}
	return ""
}

// inCeiling is true if each rule is in the agent-ceiling ClusterRole.
func inCeiling(rules []rbacv1.PolicyRule) bool {
	in := func(have, ceil []string) bool {
		return len(have) > 0 && !slices.ContainsFunc(have, func(s string) bool { return !slices.Contains(ceil, s) })
	}
	for _, rule := range rules {
		if len(rule.NonResourceURLs) > 0 || !in(rule.APIGroups, ceiling.APIGroups) ||
			!in(rule.Resources, ceiling.Resources) || !in(rule.Verbs, ceiling.Verbs) {
			return false
		}
	}
	return true
}

func appContainer(pod *corev1.PodSpec) *corev1.Container {
	i := slices.IndexFunc(pod.Containers, func(c corev1.Container) bool { return c.Name == corev1alpha1.AppContainerName })
	if i < 0 {
		return nil
	}
	return &pod.Containers[i]
}

// apply makes the key and the objects of an active AppVersion.
func (r *AppVersionReconciler) apply(ctx context.Context, av *corev1alpha1.AppVersion) (ctrl.Result, error) {
	old := av.Status.DeepCopy()
	app := &corev1alpha1.App{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: av.Spec.AppRef.Name}, app)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	gone := err != nil || !app.DeletionTimestamp.IsZero()
	switch bound := av.Status.BoundAppUID; {
	case bound == "" && gone:
		return ctrl.Result{}, r.wait(ctx, av, old, "AppNotFound", "The App of spec.appRef does not exist.")
	case bound != "" && (gone || app.UID != bound):
		// The version never binds to a different App with the same name.
		return ctrl.Result{}, r.revoke(ctx, av, "AppGone")
	}
	av.Status.BoundAppUID = app.UID

	deploy, err := r.template(ctx, av, old)
	if err != nil || deploy == nil {
		return ctrl.Result{}, err
	}
	if av.Status.KeyHash == "" {
		full, err := r.liveFull(ctx, av, app)
		if err != nil {
			return ctrl.Result{}, err
		}
		if full {
			return ctrl.Result{RequeueAfter: appLiveWait}, r.wait(ctx, av, old, "LiveVersionsFull",
				"The App has spec.maxLiveVersions versions with a key. This version waits.")
		}
	}
	key, err := r.enroll(ctx, av, old)
	if err != nil || key == "" {
		return ctrl.Result{}, err
	}
	renewErr, err := r.applyKey(ctx, av, key)
	if errors.Is(err, gateway.ErrKeyNotFound) {
		// The gateway lost a key that it had. The controller does not make the key again.
		return ctrl.Result{}, r.revoke(ctx, av, "KeyLost")
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason, err := r.applyObjects(ctx, av, deploy); err != nil {
		return ctrl.Result{}, err
	} else if reason != "" {
		return ctrl.Result{}, r.wait(ctx, av, old, reason, "The controller cannot make an object of the version.")
	}
	meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{
		Type: condProvisioned, Status: metav1.ConditionTrue, Reason: "Provisioned",
		Message: "The key, the Role, the Deployment and the Service are ready.", ObservedGeneration: av.Generation,
	})
	meta.SetStatusCondition(&av.Status.Conditions, r.Egress.condition(av.Generation))
	if err := r.writeStatus(ctx, av, old); err != nil {
		return ctrl.Result{}, err
	}
	if renewErr {
		return ctrl.Result{}, errRenewFailed
	}
	return ctrl.Result{RequeueAfter: r.untilRenew(av.Status.KeyHash)}, nil
}

// template makes the Deployment of a valid version. If the version is not valid, it writes the
// condition and gives back no Deployment.
func (r *AppVersionReconciler) template(ctx context.Context, av *corev1alpha1.AppVersion,
	old *corev1alpha1.AppVersionStatus) (*appsv1.Deployment, error) {
	msg, reason := invalidAppVersion(av), reasonInvalid
	if msg == "" {
		user, err := boundProfile(ctx, r.Client, av.Namespace)
		if err != nil {
			return nil, err
		}
		// The merge comes before the key, so that a template that is not valid makes no key.
		deploy, err := r.deployment(ctx, av, user)
		if err == nil {
			return deploy, nil
		}
		if !isInvalidTemplate(err) {
			return nil, err
		}
		msg, reason = err.Error(), reasonInvalidPodTemplate
	}
	return nil, r.wait(ctx, av, old, reason, msg)
}

// enroll adds the finalizer, makes the key Secret and writes the key hash to the status. It
// gives back the key value. If the version waits or the controller revokes it, the value is
// empty. It sets old to the status that the API server has.
func (r *AppVersionReconciler) enroll(ctx context.Context, av *corev1alpha1.AppVersion,
	old *corev1alpha1.AppVersionStatus) (string, error) {
	if controllerutil.AddFinalizer(av, appFinalizer) {
		// Update gives back the status of the API server, so keep the status in memory.
		st := av.Status.DeepCopy()
		if err := r.Update(ctx, av); err != nil {
			return "", err
		}
		av.Status = *st
	}
	// The Secret comes first. A retry after a crash finds the key value in it.
	secret, err := r.readSecret(ctx, av)
	switch {
	case errors.Is(err, errNotOwned) && av.Status.KeyHash == "":
		return "", r.wait(ctx, av, old, reasonNameConflict,
			"A Secret with the version name is not from this AppVersion.")
	case err != nil && !errors.Is(err, errNotOwned):
		return "", err
	}
	key := ""
	if secret != nil {
		key = string(secret.Data[appKeyField])
	}
	switch {
	case av.Status.KeyHash != "" && gateway.HashKey(key) != av.Status.KeyHash:
		// The Secret is gone or changed. The controller does not make a second key.
		return "", r.revoke(ctx, av, "SecretLost")
	case key == "":
		if key, err = gateway.NewKeyValue(); err != nil {
			return "", err
		}
		s := &corev1.Secret{ObjectMeta: appVersionMeta(av), Immutable: new(true),
			StringData: map[string]string{appKeyField: key}}
		if err := r.Create(ctx, s); err != nil {
			return "", err
		}
	}
	if av.Status.KeyHash == "" {
		// Write the hash before the key exists, so that a revoke can always find the key.
		av.Status.KeyAlias = "kdapp-" + string(av.UID)
		av.Status.KeyHash = gateway.HashKey(key)
		// The condition records the generation of the first key request. See applyKey.
		meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: condGatewayKey,
			Status: metav1.ConditionFalse, Reason: reasonIssuing,
			Message: "The controller makes the gateway key.", ObservedGeneration: av.Generation})
		if err := r.Status().Update(ctx, av); err != nil {
			return "", err
		}
		*old = *av.Status.DeepCopy()
	}
	return key, nil
}

// wait writes the condition Provisioned=False. The controller changes no object of the version.
func (r *AppVersionReconciler) wait(ctx context.Context, av *corev1alpha1.AppVersion,
	old *corev1alpha1.AppVersionStatus, reason, msg string) error {
	logf.FromContext(ctx).Info("The AppVersion waits", "reason", reason)
	meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{
		Type: condProvisioned, Status: metav1.ConditionFalse, Reason: reason, Message: msg,
		ObservedGeneration: av.Generation,
	})
	return r.writeStatus(ctx, av, old)
}

func (r *AppVersionReconciler) writeStatus(ctx context.Context, av *corev1alpha1.AppVersion,
	old *corev1alpha1.AppVersionStatus) error {
	if equality.Semantic.DeepEqual(*old, av.Status) {
		return nil
	}
	return r.Status().Update(ctx, av)
}

// liveFull is true if the App already has spec.maxLiveVersions other versions with a key. A
// version has a key from the enrollment until the gateway blocked the key. The controller has 1
// worker, thus 2 versions do not take the last slot at the same time.
func (r *AppVersionReconciler) liveFull(ctx context.Context, av *corev1alpha1.AppVersion,
	app *corev1alpha1.App) (bool, error) {
	list := &corev1alpha1.AppVersionList{}
	if err := r.APIReader.List(ctx, list, client.InNamespace(av.Namespace)); err != nil {
		return false, err
	}
	live := int32(0)
	for i := range list.Items {
		o := &list.Items[i]
		if o.UID != av.UID && o.Spec.AppRef.Name == app.Name && o.Status.KeyHash != "" &&
			!meta.IsStatusConditionTrue(o.Status.Conditions, corev1alpha1.AppVersionRevoked) {
			live++
		}
	}
	return live >= max(1, app.Spec.MaxLiveVersions), nil
}

// readSecret reads the key Secret. It returns nil if there is no Secret, and errNotOwned if the
// Secret is not from av or has no key.
func (r *AppVersionReconciler) readSecret(ctx context.Context, av *corev1alpha1.AppVersion) (*corev1.Secret, error) {
	s := &corev1.Secret{}
	found, err := r.getOwned(ctx, av, s)
	if !found || err != nil {
		return nil, err
	}
	if len(s.Data[appKeyField]) == 0 {
		return nil, errNotOwned
	}
	return s, nil
}

// getOwned reads the object of this kind with the version name into o. It returns false if there
// is no object, and errNotOwned if the object is not controlled by av.
func (r *AppVersionReconciler) getOwned(ctx context.Context, av *corev1alpha1.AppVersion, o client.Object) (bool, error) {
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: av.Namespace, Name: appVersionName(av)}, o)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(o, av) {
		return true, errNotOwned
	}
	return true, nil
}

// applyKey makes the key at the gateway, or renews it. The condition GatewayKey exists only
// after a CreateKey without error. Thus a missing key after that is a lost key, and applyKey
// returns gateway.ErrKeyNotFound. A new budget is in the condition only after the gateway
// accepted it. renewFailed is true if a renew failed and the key stays valid until its TTL ends.
func (r *AppVersionReconciler) applyKey(ctx context.Context, av *corev1alpha1.AppVersion, key string) (
	renewFailed bool, err error) {
	spec := gateway.KeySpec{Value: key, Alias: av.Status.KeyAlias, Model: av.Spec.Template.Model,
		Budget: av.Spec.BudgetPerDay.AsApproximateFloat64(), BudgetPeriod: 24 * time.Hour, TTL: r.keyTTL(),
		Metadata: map[string]string{metaUID: string(av.UID), metaNamespace: av.Namespace,
			"app": av.Spec.AppRef.Name, "appVersion": av.Name}}
	c := meta.FindStatusCondition(av.Status.Conditions, condGatewayKey)
	switch {
	case c == nil || c.Reason == reasonIssuing:
		if err := r.Gateway.CreateKey(ctx, spec); err != nil {
			return false, err
		}
		// CreateKey does not change a key that exists. If the spec changed after an earlier
		// request that possibly made the key, send the budget of this generation.
		if c != nil && c.ObservedGeneration != av.Generation {
			if err := r.Gateway.RenewKey(ctx, spec); err != nil {
				return false, err
			}
		}
		logf.FromContext(ctx).Info("Issued the version key", "keyAlias", av.Status.KeyAlias)
	case c.Status == metav1.ConditionTrue && c.ObservedGeneration == av.Generation &&
		r.untilRenew(av.Status.KeyHash) > 0:
		return false, nil
	default:
		err := r.Gateway.RenewKey(ctx, spec)
		if errors.Is(err, gateway.ErrKeyNotFound) {
			return false, err
		}
		if err != nil {
			logf.FromContext(ctx).Info("The renew of the version key failed", "keyAlias", av.Status.KeyAlias,
				"reason", err.Error())
			c.Status, c.Reason = metav1.ConditionFalse, reasonRenewFailed
			c.Message = "The renew of the gateway key failed. The controller tries again."
			return true, nil
		}
	}
	r.setRenewed(av.Status.KeyHash)
	meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: condGatewayKey,
		Status: metav1.ConditionTrue, Reason: "Active",
		Message: "The gateway key is ready with the budget of this generation.", ObservedGeneration: av.Generation})
	return false, nil
}

// applyObjects makes the ServiceAccount, the Role, the RoleBinding, the Service and the
// Deployment. It returns a reason if an object with the version name is not from av, or if the
// API server refuses an object.
func (r *AppVersionReconciler) applyObjects(ctx context.Context, av *corev1alpha1.AppVersion,
	deploy *appsv1.Deployment) (string, error) {
	name := appVersionName(av)
	ports := appContainer(&deploy.Spec.Template.Spec).Ports
	svcPorts := make([]corev1.ServicePort, 0, len(ports))
	for _, p := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{Name: p.Name, Port: p.ContainerPort,
			TargetPort: intstr.FromString(p.Name), Protocol: corev1.ProtocolTCP})
	}
	objs := []client.Object{
		&corev1.ServiceAccount{ObjectMeta: appVersionMeta(av), AutomountServiceAccountToken: new(false)},
		&rbacv1.Role{ObjectMeta: appVersionMeta(av), Rules: av.Spec.Template.Rules},
		&rbacv1.RoleBinding{ObjectMeta: appVersionMeta(av),
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: av.Namespace}},
		},
		&corev1.Service{ObjectMeta: appVersionMeta(av), Spec: corev1.ServiceSpec{
			Selector: appVersionLabels(av), Ports: svcPorts}},
	}
	for _, o := range objs {
		err := r.Create(ctx, o)
		switch {
		case apierrors.IsAlreadyExists(err):
			have := o.DeepCopyObject().(client.Object)
			if _, err := r.getOwned(ctx, av, have); errors.Is(err, errNotOwned) {
				return reasonNameConflict, nil
			} else if err != nil {
				return "", err
			}
			// The controller has no update grant for a Role. A Role with different rules goes, and
			// the next reconcile makes it again.
			if role, ok := have.(*rbacv1.Role); ok && !equality.Semantic.DeepEqual(role.Rules, av.Spec.Template.Rules) {
				if err := r.deleteOwned(ctx, av, &rbacv1.Role{}); err != nil {
					return "", err
				}
				return "", fmt.Errorf("the version Role had different rules, so the controller makes it again")
			}
			// The controller has no update grant for a Service. A Service with different ports
			// goes, and the next reconcile makes it again.
			if svc, ok := have.(*corev1.Service); ok && !equality.Semantic.DeepEqual(svc.Spec.Ports, svcPorts) {
				if err := r.deleteOwned(ctx, av, &corev1.Service{}); err != nil {
					return "", err
				}
				return "", fmt.Errorf("the version Service had different ports, so the controller makes it again")
			}
		case permanent(err):
			logf.FromContext(ctx).Info("The API server refused an object of the version", "kind", fmt.Sprintf("%T", o))
			return reasonRefused, nil
		case err != nil:
			return "", err
		}
	}
	have := &appsv1.Deployment{}
	found, err := r.getOwned(ctx, av, have)
	switch {
	case errors.Is(err, errNotOwned):
		return reasonNameConflict, nil
	case err != nil:
		return "", err
	case !found:
		err = r.Create(ctx, deploy)
	case have.Annotations[templateHashAnnotation] != deploy.Annotations[templateHashAnnotation] ||
		have.Spec.Replicas == nil || *have.Spec.Replicas != *deploy.Spec.Replicas:
		// The hash annotation records the last template of the controller.
		have.Spec.Template, have.Spec.Replicas = deploy.Spec.Template, deploy.Spec.Replicas
		metav1.SetMetaDataAnnotation(&have.ObjectMeta, templateHashAnnotation, deploy.Annotations[templateHashAnnotation])
		err = r.Update(ctx, have)
	}
	if permanent(err) {
		logf.FromContext(ctx).Info("The API server refused the Deployment of the version")
		return reasonRefused, nil
	}
	return "", err
}

// deployment makes the Deployment of av with the pod template of the version. The pod has the
// App labels and no Agent, Workspace or run label. It reads the key from a read-only file. It
// has a ServiceAccount token only if the version has rules.
func (r *AppVersionReconciler) deployment(ctx context.Context, av *corev1alpha1.AppVersion, user string) (
	*appsv1.Deployment, error) {
	const vol = "koodam-gateway"
	name, labels := appVersionName(av), appVersionLabels(av)
	base := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: appVersionLabels(av)}}
	addLabel(&base.ObjectMeta, UserLabel, user)
	base.Spec = corev1.PodSpec{
		ServiceAccountName:           name,
		AutomountServiceAccountToken: new(len(av.Spec.Template.Rules) > 0),
		// The key file has owner root, group appKeyGroup and mode 0440. Each container process gets
		// this group, so an app that is not root can read the key.
		SecurityContext: &corev1.PodSecurityContext{FSGroup: new(appKeyGroup)},
		Volumes: []corev1.Volume{{Name: vol, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: name, DefaultMode: new(int32(0o440))}}}},
		Containers: []corev1.Container{{
			Name: corev1alpha1.AppContainerName,
			Env: []corev1.EnvVar{
				{Name: appGatewayURLEnv, Value: r.GatewayURL},
				{Name: appModelEnv, Value: av.Spec.Template.Model},
				{Name: appKeyFileEnv, Value: workspaceGatewayDir + "/" + appKeyField},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: vol, MountPath: workspaceGatewayDir, ReadOnly: true}},
		}},
	}
	// The shared merge does not accept the ports and the probes in the main container. The app
	// container needs them, so the controller moves them around the merge. invalidAppVersion
	// examined the ports before.
	t := av.Spec.Template.PodTemplate.DeepCopy()
	u := appContainer(&t.Spec)
	ports, ready, live, start := u.Ports, u.ReadinessProbe, u.LivenessProbe, u.StartupProbe
	u.Ports, u.ReadinessProbe, u.LivenessProbe, u.StartupProbe = nil, nil, nil, nil
	tmpl, err := withPodTemplate(ctx, r.APIReader, av.Namespace, base, t, corev1alpha1.AppContainerName)
	if err != nil {
		return nil, err
	}
	c := appContainer(&tmpl.Spec)
	c.Ports, c.ReadinessProbe, c.LivenessProbe, c.StartupProbe = ports, ready, live, start
	r.Egress.apply(&tmpl.Spec, av.Namespace)
	hash, err := templateHash(tmpl)
	if err != nil {
		return nil, err
	}
	m := appVersionMeta(av)
	m.Annotations = map[string]string{templateHashAnnotation: hash}
	replicas := int32(1)
	if av.Spec.Replicas != nil {
		replicas = *av.Spec.Replicas
	}
	return &appsv1.Deployment{ObjectMeta: m, Spec: appsv1.DeploymentSpec{
		Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: tmpl}}, nil
}

// revoke blocks the key first, by the hash in the status. Then it deletes the Deployment, the
// Service and the access, writes Revoked, deletes the Secret and removes the finalizer. Each step
// can run again. If the block fails, the Secret and the finalizer stay, and Revoked is Unknown.
func (r *AppVersionReconciler) revoke(ctx context.Context, av *corev1alpha1.AppVersion, reason string) error {
	old := av.Status.DeepCopy()
	if !controllerutil.ContainsFinalizer(av, appFinalizer) {
		// The finalizer comes before the Secret and the key. Thus there is nothing to revoke.
		if !av.DeletionTimestamp.IsZero() || meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionRevoked) {
			return nil
		}
		r.setRevoke(av, reason, metav1.ConditionTrue, "There was no key to revoke.")
		return r.writeStatus(ctx, av, old)
	}
	if meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionRevoking) == nil {
		// Write the terminal state before the block, so that a retry does not renew the key.
		r.setRevoke(av, reason, metav1.ConditionFalse, "The revoke started.")
		if err := r.Status().Update(ctx, av); err != nil {
			return err
		}
	}
	secret, err := r.readSecret(ctx, av)
	if err != nil && !errors.Is(err, errNotOwned) {
		return err
	}
	hash := av.Status.KeyHash
	if hash == "" && secret != nil {
		// The Secret exists, but the status write before CreateKey did not complete.
		hash = gateway.HashKey(string(secret.Data[appKeyField]))
	}
	if hash != "" {
		if err := r.Gateway.BlockKey(ctx, hash); err != nil {
			r.setRevoke(av, reason, metav1.ConditionUnknown, "The gateway did not confirm the block. The controller tries again.")
			if serr := r.Status().Update(ctx, av); serr != nil {
				return serr
			}
			return err
		}
		r.forgetRenewed(hash)
	}
	for _, o := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &rbacv1.RoleBinding{},
		&rbacv1.Role{}, &corev1.ServiceAccount{}} {
		if err := r.deleteOwned(ctx, av, o); err != nil {
			return err
		}
	}
	if !meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionRevoked) {
		r.setRevoke(av, reason, metav1.ConditionTrue, "The key is blocked and the objects of the version are deleted.")
		if err := r.Status().Update(ctx, av); err != nil {
			return err
		}
	}
	if secret != nil {
		// The UID precondition stops the delete if the Secret changed after the read.
		uid := secret.UID
		if err := r.Delete(ctx, secret, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	controllerutil.RemoveFinalizer(av, appFinalizer)
	logf.FromContext(ctx).Info("Revoked the version key", "keyAlias", av.Status.KeyAlias, "reason", reason)
	return r.Update(ctx, av)
}

// setRevoke sets the conditions Revoking, Revoked and Provisioned of a version in the revoke.
func (r *AppVersionReconciler) setRevoke(av *corev1alpha1.AppVersion, reason string, revoked metav1.ConditionStatus,
	msg string) {
	set := func(t string, s metav1.ConditionStatus, m string) {
		meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: t, Status: s, Reason: reason,
			Message: m, ObservedGeneration: av.Generation})
	}
	set(corev1alpha1.AppVersionRevoking, metav1.ConditionTrue, "The revoke of the key started. This state is terminal.")
	set(corev1alpha1.AppVersionRevoked, revoked, msg)
	set(condProvisioned, metav1.ConditionFalse, "The version has no key.")
}

// deleteOwned deletes the object of this kind with the version name. It keeps an object that is
// not from av. The UID precondition stops the delete if the object changed after the read.
func (r *AppVersionReconciler) deleteOwned(ctx context.Context, av *corev1alpha1.AppVersion, o client.Object) error {
	found, err := r.getOwned(ctx, av, o)
	if errors.Is(err, errNotOwned) {
		logf.FromContext(ctx).Info("Kept an object with the version name that is not from this AppVersion",
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

func (r *AppVersionReconciler) keyTTL() time.Duration {
	if r.KeyTTL > 0 {
		return r.KeyTTL
	}
	return DefaultWorkspaceKeyTTL
}

func (r *AppVersionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// untilRenew returns the time until the next renew of the key with this hash. The time of the
// last renew is in memory only. Thus after a restart the controller renews each key one time.
func (r *AppVersionReconciler) untilRenew(hash string) time.Duration {
	r.renewMu.Lock()
	defer r.renewMu.Unlock()
	last, ok := r.renewed[hash]
	if !ok {
		return 0
	}
	return max(0, last.Add(r.keyTTL()/4).Sub(r.now()))
}

func (r *AppVersionReconciler) setRenewed(hash string) {
	r.renewMu.Lock()
	defer r.renewMu.Unlock()
	if r.renewed == nil {
		r.renewed = map[string]time.Time{}
	}
	r.renewed[hash] = r.now()
}

func (r *AppVersionReconciler) forgetRenewed(hash string) {
	r.renewMu.Lock()
	defer r.renewMu.Unlock()
	delete(r.renewed, hash)
}

// versionsOfApp gives the AppVersions of an App that changed, so that a version binds when its
// App appears and revokes when its App goes.
func (r *AppVersionReconciler) versionsOfApp(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &corev1alpha1.AppVersionList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the AppVersions of an App")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.AppRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.AppVersion{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1alpha1.App{}, handler.EnqueueRequestsFromMapFunc(r.versionsOfApp)).
		Named("appversion").
		Complete(r)
}
