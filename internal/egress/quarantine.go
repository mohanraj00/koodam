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

package egress

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// Holds and Profiles are the resources that the quarantine reads. The values must agree with
// api/v1alpha1.
var (
	Holds    = koodamResource("holds")
	Profiles = koodamResource("profiles")
)

// koodamResource gives the resource in the API group of Koodam.
func koodamResource(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "core.koodam.dev", Version: "v1alpha1", Resource: resource}
}

const (
	// KindControl is the kind of a span that records the start or the end of a quarantine.
	KindControl = "control"
	// ResultQuarantined and ResultReleased are the results of a control span.
	ResultQuarantined = "quarantined"
	ResultReleased    = "released"
	// ReasonQuarantined is the reason of a call that the proxy refuses during a quarantine.
	ReasonQuarantined = "quarantined"
)

// The values of a Hold. They must agree with api/v1alpha1.
const (
	holdQuarantine = "Quarantine"
	targetAgent    = "Agent"
	targetWS       = "Workspace"
	targetApp      = "App"
	targetVersion  = "AppVersion"
	targetProfile  = "Profile"
)

// quarantine is an active Hold with the action Quarantine.
type quarantine struct {
	hold, kind, namespace, name string
}

// match is true if the quarantine applies to the pod of the span. bound gives the bound namespaces
// of a Profile.
func (q quarantine) match(sp *Span, bound map[string][]string) bool {
	if q.kind == targetProfile {
		return sp.Namespace != "" && slices.Contains(bound[q.name], sp.Namespace)
	}
	if q.namespace == "" || sp.Namespace != q.namespace {
		return false
	}
	switch q.kind {
	case targetAgent:
		return sp.Agent == q.name
	case targetWS:
		return sp.Workspace == q.name
	case targetApp:
		return sp.App == q.name
	case targetVersion:
		return sp.AppVersion == q.name
	}
	return false
}

// call is an open call of a pod. stop ends it.
type call struct {
	sp   Span
	stop func()
	cut  bool
}

// Quarantines has the active Quarantine Holds and the bound namespaces of each Profile. It refuses
// each call of a pod that a quarantine matches, and it ends the open calls of a pod when its
// quarantine starts. The Holds and the Profile status come from the API server. Only an admin
// writes a Hold, and only the controller writes the Profile status. No data of a pod changes
// them. Its methods are safe for concurrent use.
type Quarantines struct {
	// Sink receives a control span when a quarantine starts and when it ends. It can be nil.
	Sink Sink
	// Now gives the time of a control span. If it is nil, the index uses time.Now.
	Now func() time.Time

	mu     sync.Mutex
	active map[string]quarantine
	bound  map[string][]string
	calls  map[*call]struct{}
}

// NewQuarantines returns an index with no quarantine.
func NewQuarantines() *Quarantines {
	return &Quarantines{active: map[string]quarantine{}, bound: map[string][]string{}, calls: map[*call]struct{}{}}
}

// Admit is the Quarantine function of a Proxy. If a quarantine matches the pod of the span, it
// gives false. Otherwise it records the call, and stop ends the call if a quarantine of the pod
// starts. The caller must call done when the call ends. done gives true if a quarantine ended
// the call.
func (q *Quarantines) Admit(sp *Span, stop func()) (done func() bool, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, h := range q.active {
		if h.match(sp, q.bound) {
			return nil, false
		}
	}
	c := &call{sp: *sp, stop: stop}
	q.calls[c] = struct{}{}
	return func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		delete(q.calls, c)
		return c.cut
	}, true
}

// setHold reads a Hold. A Hold with the action Quarantine and no release is active.
func (q *Quarantines) setHold(u *unstructured.Unstructured) {
	action, _, _ := unstructured.NestedString(u.Object, "spec", "action")
	release, released, _ := unstructured.NestedMap(u.Object, "spec", "release")
	if action != holdQuarantine {
		return
	}
	if released {
		user, _ := release["user"].(string)
		reason, _ := release["reason"].(string)
		q.end(u.GetName(), user, reason)
		return
	}
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "target", "kind")
	namespace, _, _ := unstructured.NestedString(u.Object, "spec", "target", "namespace")
	name, _, _ := unstructured.NestedString(u.Object, "spec", "target", "name")
	user, _, _ := unstructured.NestedString(u.Object, "spec", "user")
	reason, _, _ := unstructured.NestedString(u.Object, "spec", "reason")
	h := quarantine{hold: u.GetName(), kind: kind, namespace: namespace, name: name}
	q.mu.Lock()
	_, known := q.active[h.hold]
	q.active[h.hold] = h
	stops := q.cutLocked()
	q.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	if !known {
		q.write(h, ResultQuarantined, user, reason)
	}
}

// end removes an active quarantine and writes its released span. A Hold that is not active
// writes nothing.
func (q *Quarantines) end(hold, user, reason string) {
	q.mu.Lock()
	h, known := q.active[hold]
	delete(q.active, hold)
	q.mu.Unlock()
	if known && user != "" {
		q.write(h, ResultReleased, user, reason)
	}
}

// setProfile reads the bound namespaces of a Profile from its status.
func (q *Quarantines) setProfile(u *unstructured.Unstructured) {
	bound, _, _ := unstructured.NestedStringSlice(u.Object, "status", "boundNamespaces")
	q.mu.Lock()
	q.bound[u.GetName()] = bound
	stops := q.cutLocked()
	q.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

func (q *Quarantines) removeProfile(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.bound, name)
}

// cutLocked marks each open call that a quarantine matches, and gives the stop functions. The
// caller holds the lock and calls the functions after it releases the lock.
func (q *Quarantines) cutLocked() []func() {
	var stops []func()
	for c := range q.calls {
		if c.cut {
			continue
		}
		for _, h := range q.active {
			if h.match(&c.sp, q.bound) {
				c.cut = true
				stops = append(stops, c.stop)
				break
			}
		}
	}
	return stops
}

// write writes the control span of a quarantine. by is the user that started or released it. A
// Profile quarantine writes one span for each bound namespace that the index knows.
func (q *Quarantines) write(h quarantine, result, by, reason string) {
	if q.Sink == nil {
		return
	}
	now := time.Now
	if q.Now != nil {
		now = q.Now
	}
	sp := Span{V: SpanVersion, Time: now().UTC(), Kind: KindControl, Result: result, Reason: reason, By: by,
		Namespace: h.namespace}
	switch h.kind {
	case targetAgent:
		sp.Agent = h.name
	case targetWS:
		sp.Workspace = h.name
	case targetApp:
		sp.App = h.name
	case targetVersion:
		sp.AppVersion = h.name
	case targetProfile:
		q.mu.Lock()
		bound := slices.Clone(q.bound[h.name])
		q.mu.Unlock()
		for _, ns := range bound {
			sp.Namespace = ns
			q.Sink.Write(sp)
		}
		return
	}
	q.Sink.Write(sp)
}

// Watch fills the index from one informer on the Holds and one informer on the Profiles. Both are
// cluster-scoped. It needs get, list and watch on holds and profiles. It returns when the caches
// are in sync, and the informers stop when ctx ends.
func (q *Quarantines) Watch(ctx context.Context, client dynamic.Interface, resync time.Duration) error {
	f := dynamicinformer.NewDynamicSharedInformerFactory(client, resync)
	handler := func(set func(*unstructured.Unstructured), remove func(name string)) cache.ResourceEventHandlerFuncs {
		apply := func(o any) {
			if u, ok := o.(*unstructured.Unstructured); ok {
				set(u)
			}
		}
		return cache.ResourceEventHandlerFuncs{
			AddFunc:    apply,
			UpdateFunc: func(_, o any) { apply(o) },
			DeleteFunc: func(o any) {
				if d, ok := o.(cache.DeletedFinalStateUnknown); ok {
					o = d.Obj
				}
				if u, ok := o.(*unstructured.Unstructured); ok {
					remove(u.GetName())
				}
			},
		}
	}
	holds := f.ForResource(Holds).Informer()
	// A Hold delete needs a release first. Thus a deleted Hold is not active, and it writes no span.
	if _, err := holds.AddEventHandler(handler(q.setHold, func(name string) { q.end(name, "", "") })); err != nil {
		return err
	}
	profiles := f.ForResource(Profiles).Informer()
	if _, err := profiles.AddEventHandler(handler(q.setProfile, q.removeProfile)); err != nil {
		return err
	}
	f.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), holds.HasSynced, profiles.HasSynced) {
		return errors.New("the Hold and the Profile caches did not sync")
	}
	return nil
}
