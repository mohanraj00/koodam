/*
Copyright 2026.

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
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	reasonStopping      = "Stopping"
	reasonQuarantining  = "Quarantining"
	reasonNoEgressProxy = "NoEgressProxy"
	reasonUnsupported   = "Unsupported"
	reasonAppsDisabled  = "AppsDisabled"
	// msgVersionsLeft is the message of a Profile Hold with an AppVersion and no App controllers.
	msgVersionsLeft = "A namespace of the Profile has an AppVersion, and the App controllers do not run, " +
		"so no controller stops or quarantines it. Retire each AppVersion, or enable the App controllers."
	// appsDisabledRecheck is the time until a Profile Hold reads the AppVersions again.
	appsDisabledRecheck = time.Minute
)

// HoldReconciler writes the Held condition of each Hold. It does not stop a workload: the
// reconciler of each kind reads the Holds and uses its own revoke path. Thus the key block always
// comes before the pod stop.
type HoldReconciler struct {
	client.Client
	// Recorder writes the events of the Holds. If it is nil, the controller writes no event.
	Recorder events.EventRecorder
	// AppsEnabled is true if the App controllers run. Without them, no controller stops an App or
	// an AppVersion.
	AppsEnabled bool
	// APIReader reads the workloads with no cache. Thus each install of the cluster counts the
	// workloads of all the namespaces of a Profile, and each install writes the same status. If it
	// is nil, the controller uses the client.
	APIReader client.Reader
	// Namespaces are the namespaces that the manager watches. An empty list is each namespace. The
	// controller writes the status of a Hold on one workload or one App only if it watches the
	// namespace of the target. The install of that namespace writes it.
	Namespaces []string
	// EgressProxy is true if the pods send their egress through the egress proxy. Only the proxy
	// refuses the calls of a quarantined pod, so a Quarantine Hold needs it.
	EgressProxy bool
}

// +kubebuilder:rbac:groups=core.koodam.dev,resources=holds,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.koodam.dev,resources=holds/status,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core.koodam.dev,resources=appversions,verbs=get;list

// Reconcile writes the Held condition from the state of the workloads of the target.
func (r *HoldReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	h := &corev1alpha1.Hold{}
	if err := r.Get(ctx, req.NamespacedName, h); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if t := h.Spec.Target; t.Kind != corev1alpha1.HoldTargetProfile && !watched(r.Namespaces, t.Namespace) {
		return ctrl.Result{}, nil
	}
	cover := slices.Clone(h.Status.Namespaces)
	status, reason, msg, err := r.state(ctx, h)
	if err != nil {
		return ctrl.Result{}, err
	}
	var result ctrl.Result
	if reason == reasonAppsDisabled && h.Spec.Target.Kind == corev1alpha1.HoldTargetProfile {
		// No controller watches the AppVersions, so the Hold reads them again after a time.
		result.RequeueAfter = appsDisabledRecheck
	}
	old := meta.FindStatusCondition(h.Status.Conditions, corev1alpha1.HoldHeld)
	if old != nil && old.Status == status && old.Reason == reason && old.Message == msg &&
		old.ObservedGeneration == h.Generation && slices.Equal(cover, h.Status.Namespaces) {
		return result, nil
	}
	changed := old == nil || old.Reason != reason
	meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: corev1alpha1.HoldHeld, Status: status,
		Reason: reason, Message: msg, ObservedGeneration: h.Generation})
	if err := r.Status().Update(ctx, h); err != nil {
		return ctrl.Result{}, err
	}
	if changed && r.Recorder != nil {
		switch reason {
		case reasonStopped:
			r.Recorder.Eventf(h, nil, corev1.EventTypeNormal, reason, actionStop, "%s", eventNote(msg))
		case reasonQuarantined:
			r.Recorder.Eventf(h, nil, corev1.EventTypeNormal, reason, actionQuarantine, "%s", eventNote(msg))
		case reasonReleased:
			r.Recorder.Eventf(h, nil, corev1.EventTypeNormal, reason, actionRelease, "%s", eventNote(msg))
		case reasonNoEgressProxy:
			r.Recorder.Eventf(h, nil, corev1.EventTypeWarning, reason, actionQuarantine, "%s", eventNote(msg))
		}
	}
	logf.FromContext(ctx).Info("Wrote the Held condition", "hold", h.Name, "reason", reason)
	return result, nil
}

// state gives the status, the reason and the message of the Held condition.
func (r *HoldReconciler) state(ctx context.Context, h *corev1alpha1.Hold) (metav1.ConditionStatus, string, string,
	error) {
	switch {
	case h.Spec.Release != nil:
		return metav1.ConditionFalse, reasonReleased, fmt.Sprintf("The user %s released the Hold. Reason: %s",
			h.Spec.Release.User, h.Spec.Release.Reason), nil
	case h.Spec.Action == corev1alpha1.HoldQuarantine:
		return r.quarantineState(ctx, h)
	case h.Spec.Action != corev1alpha1.HoldStop:
		return metav1.ConditionFalse, reasonUnsupported, "This controller version does not know the action.", nil
	}
	t := h.Spec.Target
	if !r.AppsEnabled && (t.Kind == corev1alpha1.HoldTargetApp || t.Kind == corev1alpha1.HoldTargetAppVersion) {
		return metav1.ConditionFalse, reasonAppsDisabled,
			"The App controllers do not run, so no controller stops an App or an AppVersion.", nil
	}
	if left, err := r.versionsLeft(ctx, t); err != nil || left {
		return metav1.ConditionFalse, reasonAppsDisabled, msgVersionsLeft, err
	}
	total, waiting, err := r.count(ctx, t, stopDone)
	if err != nil {
		return "", "", "", err
	}
	switch {
	case waiting > 0:
		return metav1.ConditionFalse, reasonStopping,
			fmt.Sprintf("%d of the %d workloads of the target wait for the stop.", waiting, total), nil
	case total == 0:
		return metav1.ConditionTrue, reasonStopped, "No workload of the target exists.", nil
	}
	return metav1.ConditionTrue, reasonStopped,
		fmt.Sprintf("The %d workloads of the target are stopped.", total), nil
}

// quarantineState gives the Held condition of a Hold with the action Quarantine.
func (r *HoldReconciler) quarantineState(ctx context.Context, h *corev1alpha1.Hold) (metav1.ConditionStatus,
	string, string, error) {
	t := h.Spec.Target
	profile := t.Kind == corev1alpha1.HoldTargetProfile
	switch {
	case !profile && !r.EgressProxy:
		return metav1.ConditionFalse, reasonNoEgressProxy,
			"The controller has no egress proxy, so no component refuses the calls of a quarantined pod. " +
				"Use the action Stop, or enable the egress proxy.", nil
	case !r.AppsEnabled && (t.Kind == corev1alpha1.HoldTargetApp || t.Kind == corev1alpha1.HoldTargetAppVersion):
		return metav1.ConditionFalse, reasonAppsDisabled,
			"The App controllers do not run, so no controller quarantines an App or an AppVersion.", nil
	}
	if profile {
		// A Profile can bind the namespaces of 2 installs. The egress proxy of one install does not
		// refuse the calls of the pods of the other install.
		missing, without, err := r.proxyCover(ctx, h)
		switch {
		case err != nil:
			return "", "", "", err
		case len(without) > 0:
			return metav1.ConditionFalse, reasonNoEgressProxy, fmt.Sprintf("The install of each of these namespaces has "+
				"no egress proxy, so no component refuses the calls of their pods: %s. Use the action Stop, or "+
				"enable the egress proxy in each install.", strings.Join(without, ", ")), nil
		case len(missing) > 0:
			return metav1.ConditionFalse, reasonQuarantining, fmt.Sprintf("These namespaces wait for the record of "+
				"the manager of their install: %s.", strings.Join(missing, ", ")), nil
		}
	}
	if left, err := r.versionsLeft(ctx, t); err != nil || left {
		return metav1.ConditionFalse, reasonAppsDisabled, msgVersionsLeft, err
	}
	total, waiting, err := r.count(ctx, t, quarantineDone)
	if err != nil {
		return "", "", "", err
	}
	switch {
	case waiting > 0:
		return metav1.ConditionFalse, reasonQuarantining,
			fmt.Sprintf("%d of the %d workloads of the target wait for the quarantine.", waiting, total), nil
	case total == 0:
		return metav1.ConditionTrue, reasonQuarantined, "No workload of the target exists.", nil
	}
	return metav1.ConditionTrue, reasonQuarantined,
		fmt.Sprintf("The %d workloads of the target are quarantined.", total), nil
}

// proxyCover writes the records of the egress proxy cover into the status of the Hold, in memory.
// The manager writes a record for each bound namespace that it watches. It keeps the record of
// each other bound namespace, because the manager of that install writes it, and it removes the
// record of a namespace that the Profile does not bind. The records have the order of the bound
// namespaces, so each manager writes the same list. proxyCover returns the bound namespaces with
// no record, and those of an install with no egress proxy.
func (r *HoldReconciler) proxyCover(ctx context.Context, h *corev1alpha1.Hold) (missing, without []string,
	err error) {
	have := map[string]bool{}
	for _, rec := range h.Status.Namespaces {
		have[rec.Name] = rec.EgressProxy
	}
	var records []corev1alpha1.HoldNamespaceStatus
	err = r.eachBound(ctx, h.Spec.Target.Name, func(ns string) error {
		proxy, ok := have[ns]
		if watched(r.Namespaces, ns) {
			proxy, ok = r.EgressProxy, true
		}
		switch {
		case !ok:
			missing = append(missing, ns)
			return nil
		case !proxy:
			without = append(without, ns)
		}
		records = append(records, corev1alpha1.HoldNamespaceStatus{Name: ns, EgressProxy: proxy})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	h.Status.Namespaces = records
	return missing, without, nil
}

// doneCheck tells if the action of a Hold is complete for a workload of each kind.
type doneCheck struct {
	agent     func(*corev1alpha1.Agent) bool
	workspace func(*corev1alpha1.Workspace) bool
	version   func(*corev1alpha1.AppVersion) bool
}

var (
	stopDone       = doneCheck{agentStopped, workspaceStopped, appVersionStopped}
	quarantineDone = doneCheck{agentQuarantined, workspaceQuarantined, appVersionQuarantined}
)

// count gives the number of the workloads of the target, and the number for which the action is
// not complete.
func (r *HoldReconciler) count(ctx context.Context, t corev1alpha1.HoldTarget, d doneCheck) (total, waiting int,
	err error) {
	add := func(stopped bool) {
		total++
		if !stopped {
			waiting++
		}
	}
	rd := r.reader()
	key := types.NamespacedName{Namespace: t.Namespace, Name: t.Name}
	switch t.Kind {
	case corev1alpha1.HoldTargetAgent:
		a := &corev1alpha1.Agent{}
		if err := rd.Get(ctx, key, a); err != nil {
			return 0, 0, client.IgnoreNotFound(err)
		}
		add(d.agent(a))
	case corev1alpha1.HoldTargetWorkspace:
		ws := &corev1alpha1.Workspace{}
		if err := rd.Get(ctx, key, ws); err != nil {
			return 0, 0, client.IgnoreNotFound(err)
		}
		add(d.workspace(ws))
	case corev1alpha1.HoldTargetAppVersion:
		av := &corev1alpha1.AppVersion{}
		if err := rd.Get(ctx, key, av); err != nil {
			return 0, 0, client.IgnoreNotFound(err)
		}
		add(d.version(av))
	case corev1alpha1.HoldTargetApp:
		list := &corev1alpha1.AppVersionList{}
		if err := rd.List(ctx, list, client.InNamespace(t.Namespace)); err != nil {
			return 0, 0, err
		}
		for i := range list.Items {
			if list.Items[i].Spec.AppRef.Name == t.Name {
				add(d.version(&list.Items[i]))
			}
		}
	case corev1alpha1.HoldTargetProfile:
		return r.countProfile(ctx, t.Name, d)
	}
	return total, waiting, nil
}

// countProfile counts the workloads in each namespace that the Profile binds, also in the
// namespaces of a different install. It counts the AppVersions only if the App controllers run.
func (r *HoldReconciler) countProfile(ctx context.Context, name string, d doneCheck) (total, waiting int,
	err error) {
	add := func(stopped bool) {
		total++
		if !stopped {
			waiting++
		}
	}
	rd := r.reader()
	err = r.eachBound(ctx, name, func(ns string) error {
		agents := &corev1alpha1.AgentList{}
		if err := rd.List(ctx, agents, client.InNamespace(ns)); err != nil {
			return err
		}
		for i := range agents.Items {
			add(d.agent(&agents.Items[i]))
		}
		workspaces := &corev1alpha1.WorkspaceList{}
		if err := rd.List(ctx, workspaces, client.InNamespace(ns)); err != nil {
			return err
		}
		for i := range workspaces.Items {
			add(d.workspace(&workspaces.Items[i]))
		}
		if !r.AppsEnabled {
			return nil
		}
		versions := &corev1alpha1.AppVersionList{}
		if err := rd.List(ctx, versions, client.InNamespace(ns)); err != nil {
			return err
		}
		for i := range versions.Items {
			add(d.version(&versions.Items[i]))
		}
		return nil
	})
	return total, waiting, err
}

// versionsLeft is true if the App controllers do not run and a namespace that the Profile of the
// target binds has an AppVersion. No controller then stops or quarantines that AppVersion. The
// read has no cache, because the manager has no AppVersion watch without the App controllers.
func (r *HoldReconciler) versionsLeft(ctx context.Context, t corev1alpha1.HoldTarget) (bool, error) {
	if r.AppsEnabled || t.Kind != corev1alpha1.HoldTargetProfile {
		return false, nil
	}
	left := false
	err := r.eachBound(ctx, t.Name, func(ns string) error {
		versions := &corev1alpha1.AppVersionList{}
		if err := r.reader().List(ctx, versions, client.InNamespace(ns), client.Limit(1)); err != nil {
			return err
		}
		left = left || len(versions.Items) > 0
		return nil
	})
	return left, err
}

// eachBound calls fn for each namespace of the Profile that the Profile binds. It does nothing if
// the Profile does not exist.
func (r *HoldReconciler) eachBound(ctx context.Context, name string, fn func(ns string) error) error {
	p := &corev1alpha1.Profile{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
		return client.IgnoreNotFound(err)
	}
	for _, ns := range p.Spec.Namespaces {
		bound, err := boundProfile(ctx, r.Client, ns)
		if err != nil {
			return err
		}
		if bound != name {
			continue
		}
		if err := fn(ns); err != nil {
			return err
		}
	}
	return nil
}

// reader gives the API reader, or the client if the API reader is nil.
func (r *HoldReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// agentStopped is true if the run ended and its revoke is complete.
func agentStopped(a *corev1alpha1.Agent) bool {
	ended := a.Status.Phase == corev1alpha1.AgentSucceeded || a.Status.Phase == corev1alpha1.AgentFailed
	return ended && !controllerutil.ContainsFinalizer(a, finalizer)
}

// workspaceStopped is true if a Hold stopped the Workspace: its key is revoked and its pod is
// stopped.
func workspaceStopped(ws *corev1alpha1.Workspace) bool {
	return heldWorkspace(ws) && ws.Status.Phase == corev1alpha1.WorkspaceSuspended
}

// appVersionStopped is true if the revoke of the version is complete.
func appVersionStopped(av *corev1alpha1.AppVersion) bool {
	return meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionRevoked) &&
		!controllerutil.ContainsFinalizer(av, appFinalizer)
}

// stopHolds maps a change of a workload to each Hold with no release.
func (r *HoldReconciler) stopHolds(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &corev1alpha1.HoldList{}
	if err := r.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the Holds")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if activeHold(&list.Items[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *HoldReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.Hold{}).
		Watches(&corev1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(r.stopHolds)).
		Watches(&corev1alpha1.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.stopHolds))
	if r.AppsEnabled {
		b = b.Watches(&corev1alpha1.AppVersion{}, handler.EnqueueRequestsFromMapFunc(r.stopHolds))
	}
	return b.Named("hold").Complete(r)
}
