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
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// reasonStopped is the reason of the stop of a workload by a Hold.
	reasonStopped = "Stopped"
	// reasonHeld is the reason of the Ready condition of a Workspace that a Hold stops.
	reasonHeld = "Held"
	// reasonReleased is the reason of a Hold with spec.release.
	reasonReleased = "Released"
	// reasonQuarantined is the reason of the quarantine of a workload by a Hold.
	reasonQuarantined = "Quarantined"
	// reasonTestKeysBlocking is the reason of a Workspace that waits for the block of its test keys.
	reasonTestKeysBlocking = "TestKeysBlocking"
	// actionStop, actionQuarantine and actionRelease are the actions of the events of a Hold.
	actionStop       = "Stop"
	actionQuarantine = "Quarantine"
	actionRelease    = "Release"
	// quarantineFinalizer keeps a quarantined workload and its pods until the release. Then the
	// controller stops the workload, and it removes the finalizer.
	quarantineFinalizer = "koodam.dev/quarantine"
	// maxEventNote is less than the limit of the note of an events.k8s.io/v1 Event (1 kB).
	maxEventNote = 1000
)

// stopHold returns the Hold with the action Stop and no release that targets a workload. It
// gives the oldest Hold if more than one matches. kind is the kind of the workload. app is the
// App of an AppVersion, else "". It returns nil if no Hold stops the workload.
func stopHold(ctx context.Context, c client.Reader, kind corev1alpha1.HoldTargetKind,
	namespace, name, app string) (*corev1alpha1.Hold, error) {
	return targetHold(ctx, c, corev1alpha1.HoldStop, kind, namespace, name, app)
}

// quarantineHold is stopHold for the action Quarantine.
func quarantineHold(ctx context.Context, c client.Reader, kind corev1alpha1.HoldTargetKind,
	namespace, name, app string) (*corev1alpha1.Hold, error) {
	return targetHold(ctx, c, corev1alpha1.HoldQuarantine, kind, namespace, name, app)
}

// targetHold returns the oldest Hold with the action and no release that targets the workload.
func targetHold(ctx context.Context, c client.Reader, action corev1alpha1.HoldAction,
	kind corev1alpha1.HoldTargetKind, namespace, name, app string) (*corev1alpha1.Hold, error) {
	list := &corev1alpha1.HoldList{}
	if err := c.List(ctx, list); err != nil {
		return nil, err
	}
	var profile *string
	var found *corev1alpha1.Hold
	for i := range list.Items {
		h := &list.Items[i]
		if h.Spec.Release != nil || h.Spec.Action != action {
			continue
		}
		t := h.Spec.Target
		var match bool
		switch t.Kind {
		case corev1alpha1.HoldTargetProfile:
			if profile == nil {
				p, err := boundProfile(ctx, c, namespace)
				if err != nil {
					return nil, err
				}
				profile = &p
			}
			match = *profile != "" && t.Name == *profile
		case corev1alpha1.HoldTargetApp:
			match = app != "" && t.Namespace == namespace && t.Name == app
		default:
			match = t.Kind == kind && t.Namespace == namespace && t.Name == name
		}
		if match && (found == nil || olderHold(h, found)) {
			found = h
		}
	}
	return found, nil
}

// heldWorkspace is true while a Hold stops the Workspace. The Workspace controller writes the
// condition at the start of each reconcile.
func heldWorkspace(ws *corev1alpha1.Workspace) bool {
	return meta.IsStatusConditionTrue(ws.Status.Conditions, corev1alpha1.HoldHeld)
}

// activeHold is true for a Hold with no release. The action is Stop or Quarantine.
func activeHold(h *corev1alpha1.Hold) bool { return h.Spec.Release == nil }

// quarantineEvent writes a Warning event on the workload, with the Hold as the related object.
// rec can be nil.
func quarantineEvent(rec events.EventRecorder, obj runtime.Object, h *corev1alpha1.Hold) {
	if rec == nil {
		return
	}
	rec.Eventf(obj, h, corev1.EventTypeWarning, reasonQuarantined, actionQuarantine, "%s",
		eventNote(fmt.Sprintf("The Hold %s quarantines this workload. User: %s. Reason: %s", h.Name, h.Spec.User,
			h.Spec.Reason)))
}

func olderHold(x, y *corev1alpha1.Hold) bool {
	if !x.CreationTimestamp.Equal(&y.CreationTimestamp) {
		return x.CreationTimestamp.Before(&y.CreationTimestamp)
	}
	return x.Name < y.Name
}

// holdEvent writes a Warning event on the workload, with the Hold as the related object. The
// note has the reason and the user of the Hold. rec can be nil.
func holdEvent(rec events.EventRecorder, obj runtime.Object, h *corev1alpha1.Hold) {
	if rec == nil {
		return
	}
	rec.Eventf(obj, h, corev1.EventTypeWarning, reasonStopped, actionStop, "%s",
		eventNote(fmt.Sprintf("The Hold %s stops this workload. User: %s. Reason: %s", h.Name, h.Spec.User, h.Spec.Reason)))
}

// eventNote cuts s to maxEventNote bytes at a rune boundary.
func eventNote(s string) string {
	if len(s) <= maxEventNote {
		return s
	}
	s = s[:maxEventNote]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// watched is true if the manager watches the namespace. An empty list is each namespace. A
// manager with a list does not see the namespaced objects of the other namespaces, because they
// belong to a different install of the cluster.
func watched(namespaces []string, ns string) bool {
	return len(namespaces) == 0 || slices.Contains(namespaces, ns)
}

// holdRequests gives a request for each workload of the kind that the Hold can target. For a
// Profile, it gives each workload of the kind in each namespace of spec.namespaces. The
// reconciler of the workload then reads the binding. It gives no request for a namespace that the
// manager does not watch: the install of that namespace stops its own workloads.
func holdRequests(ctx context.Context, c client.Reader, h *corev1alpha1.Hold,
	kind corev1alpha1.HoldTargetKind, namespaces []string) []reconcile.Request {
	t := h.Spec.Target
	switch {
	case t.Kind == corev1alpha1.HoldTargetProfile:
		p := &corev1alpha1.Profile{}
		if err := c.Get(ctx, types.NamespacedName{Name: t.Name}, p); err != nil {
			return nil
		}
		return namespaceRequests(ctx, c, kind, p.Spec.Namespaces, namespaces)
	case !watched(namespaces, t.Namespace):
		return nil
	case t.Kind == kind:
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: t.Namespace, Name: t.Name}}}
	case t.Kind == corev1alpha1.HoldTargetApp && kind == corev1alpha1.HoldTargetAppVersion:
		return appVersionRequests(ctx, c, t.Namespace, t.Name)
	}
	return nil
}

// namespaceRequests gives a request for each workload of the kind in each namespace of list that
// the manager watches. If the list of one namespace fails, it logs the error and keeps the
// requests of the other namespaces.
func namespaceRequests(ctx context.Context, c client.Reader, kind corev1alpha1.HoldTargetKind,
	list, namespaces []string) []reconcile.Request {
	var reqs []reconcile.Request
	for _, ns := range list {
		if !watched(namespaces, ns) {
			continue
		}
		r, err := workloadRequests(ctx, c, kind, ns)
		if err != nil {
			logf.FromContext(ctx).Error(err, "Failed to list the workloads of a namespace", "namespace", ns)
			continue
		}
		reqs = append(reqs, r...)
	}
	return reqs
}

// appVersionRequests gives a request for each AppVersion of the App.
func appVersionRequests(ctx context.Context, c client.Reader, namespace, app string) []reconcile.Request {
	list := &corev1alpha1.AppVersionList{}
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list the AppVersions of an App", "app", app)
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.AppRef.Name == app {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// workloadRequests gives a request for each workload of the kind in the namespace.
func workloadRequests(ctx context.Context, c client.Reader, kind corev1alpha1.HoldTargetKind,
	namespace string) ([]reconcile.Request, error) {
	var list client.ObjectList
	switch kind {
	case corev1alpha1.HoldTargetAgent:
		list = &corev1alpha1.AgentList{}
	case corev1alpha1.HoldTargetWorkspace:
		list = &corev1alpha1.WorkspaceList{}
	case corev1alpha1.HoldTargetAppVersion:
		list = &corev1alpha1.AppVersionList{}
	default:
		return nil, nil
	}
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var reqs []reconcile.Request
	err := meta.EachListItem(list, func(o runtime.Object) error {
		if m, ok := o.(client.Object); ok {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(m)})
		}
		return nil
	})
	return reqs, err
}

// holdMapper maps a Hold to the workloads of the kind that it can target. A release also maps,
// so that a Workspace starts again. namespaces are the namespaces that the manager watches.
func holdMapper(c client.Reader, kind corev1alpha1.HoldTargetKind, namespaces []string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		h, ok := obj.(*corev1alpha1.Hold)
		if !ok {
			return nil
		}
		return holdRequests(ctx, c, h, kind, namespaces)
	}
}

// profileHoldMapper maps a Namespace or a Profile to the workloads of the kind in it, but only
// while a Hold with no release targets a Profile. Thus a namespace that a held Profile binds
// later also stops. namespaces are the namespaces that the manager watches.
func profileHoldMapper(c client.Reader, kind corev1alpha1.HoldTargetKind, namespaces []string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := &corev1alpha1.HoldList{}
		if err := c.List(ctx, list); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to list the Holds")
			return nil
		}
		var names []string
		for i := range list.Items {
			h := &list.Items[i]
			if !activeHold(h) || h.Spec.Target.Kind != corev1alpha1.HoldTargetProfile {
				continue
			}
			switch o := obj.(type) {
			case *corev1.Namespace:
				names = []string{o.Name}
			case *corev1alpha1.Profile:
				if o.Name == h.Spec.Target.Name {
					names = o.Spec.Namespaces
				}
			}
			if names != nil {
				break
			}
		}
		return namespaceRequests(ctx, c, kind, names, namespaces)
	}
}
