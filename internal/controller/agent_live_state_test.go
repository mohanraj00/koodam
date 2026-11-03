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

// These tests show that the Agent controller decides from the live Agent and the live run Job, not
// from the cache. They use a fake API, a cache that returns old objects and a fake gateway that
// counts the calls. They use no cluster, no real gateway and no real key. The tests make a key in
// memory only, and each failure message is fixed text.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

var ingKey = types.NamespacedName{Namespace: "team", Name: "ing"}

// countGateway counts the gateway calls and keeps their order. It keeps no key value.
type countGateway struct {
	live    client.Reader
	order   []string
	creates int
	// unsafe counts each CreateKey when the live Agent did not have the finalizer and the hash of
	// the key.
	unsafe int
	// blocked has the key hashes of the BlockKey calls.
	blocked []string
}

func (g *countGateway) CreateKey(ctx context.Context, s gateway.KeySpec) error {
	a := &corev1alpha1.Agent{}
	if g.live.Get(ctx, ingKey, a) != nil || a.Status.KeyHash != gateway.HashKey(s.Value) ||
		!slices.Contains(a.Finalizers, finalizer) {
		g.unsafe++
	}
	g.creates++
	g.order = append(g.order, "create")
	return nil
}

func (g *countGateway) BlockKey(_ context.Context, key string) error {
	g.blocked = append(g.blocked, key)
	g.order = append(g.order, "block")
	return nil
}

func (g *countGateway) RenewKey(context.Context, gateway.KeySpec) error { return nil }

func (g *countGateway) Usage(context.Context, string) (gateway.Usage, error) {
	g.order = append(g.order, "usage")
	return gateway.Usage{}, nil
}

// ingWorld returns a fake API server with objs.
func ingWorld(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if clientgoscheme.AddToScheme(s) != nil || corev1alpha1.AddToScheme(s) != nil {
		t.Fatal("the test could not make the scheme")
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&corev1alpha1.Agent{}, &batchv1.Job{}).Build()
}

func ingReconciler(c client.Client, rd client.Reader, g *countGateway) *AgentReconciler {
	return &AgentReconciler{Client: c, Scheme: c.Scheme(), APIReader: rd, Gateway: g,
		GatewayURL: "http://gw.test:4000", Grace: time.Minute, JobTTL: 3600}
}

func ingReconcile(t *testing.T, r *AgentReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: ingKey}); err != nil {
		t.Fatal("the reconcile returned an error")
	}
}

// ingAgent returns an ordinary Agent.
func ingAgent() *corev1alpha1.Agent {
	return &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: ingKey.Name, Namespace: ingKey.Namespace, UID: "uid-live"},
		Spec: corev1alpha1.AgentSpec{Image: "img", Command: []string{"run"}, Model: "m",
			Budget: resource.MustParse("1"), DeadlineSeconds: 60},
	}
}

// ingEnroll writes the controller state of a run attempt to a: the finalizer and the key hash. It
// returns the run Secret with the key.
func ingEnroll(t *testing.T, a *corev1alpha1.Agent, phase corev1alpha1.AgentPhase) *corev1.Secret {
	t.Helper()
	key, err := gateway.NewKeyValue()
	if err != nil {
		t.Fatal("the test could not make a key")
	}
	a.Finalizers = []string{finalizer}
	a.Status = corev1alpha1.AgentStatus{Phase: phase, KeyAlias: "kd-" + string(a.UID), KeyHash: gateway.HashKey(key)}
	if phase == corev1alpha1.AgentRunning {
		a.Status.Job = runName(a)
	}
	return &corev1.Secret{ObjectMeta: runMeta(a), Data: map[string][]byte{keyField: []byte(key)}}
}

// ingJob returns the run Job of a. The Job did not end.
func ingJob(a *corev1alpha1.Agent) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: runMeta(a)}
}

// ingLive returns the live Agent, or nil if it is gone.
func ingLive(t *testing.T, rd client.Reader) *corev1alpha1.Agent {
	t.Helper()
	a := &corev1alpha1.Agent{}
	if err := rd.Get(t.Context(), ingKey, a); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		t.Fatal("the test could not read the Agent")
	}
	return a
}

// ingHas reports if the live API has the run object of this kind.
func ingHas(t *testing.T, rd client.Reader, o client.Object) bool {
	t.Helper()
	err := rd.Get(t.Context(), types.NamespacedName{Namespace: ingKey.Namespace, Name: "kd-" + ingKey.Name}, o)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal("the test could not read a run object")
	}
	return err == nil
}

// ingNoAccess stops the test if the live API has a ServiceAccount, a Role, a RoleBinding or a Job.
func ingNoAccess(t *testing.T, rd client.Reader) {
	t.Helper()
	for _, o := range []client.Object{&corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{}, &batchv1.Job{}} {
		if ingHas(t, rd, o) {
			t.Fatal("the controller made a run object")
		}
	}
}

// ingExpectRunning stops the test if the run did not start with one CreateKey after the checkpoint.
func ingExpectRunning(t *testing.T, rd client.Reader, g *countGateway) {
	t.Helper()
	a := ingLive(t, rd)
	switch {
	case g.creates != 1 || g.unsafe != 0 || len(g.blocked) != 0:
		t.Fatal("the run did not make one key after the stored hash and finalizer")
	case a == nil || a.Status.Phase != corev1alpha1.AgentRunning ||
		!meta.IsStatusConditionTrue(a.Status.Conditions, condProvisioned):
		t.Fatal("the Agent is not Running")
	case !ingHas(t, rd, &corev1.Secret{}) || !ingHas(t, rd, &rbacv1.RoleBinding{}) || !ingHas(t, rd, &batchv1.Job{}):
		t.Fatal("a run object is missing")
	}
}

// ingExpectStillRunning stops the test if the controller called the gateway, wrote the Agent or
// deleted a run object of a Running run. before is the live Agent before the reconcile.
func ingExpectStillRunning(t *testing.T, rd client.Reader, g *countGateway, before *corev1alpha1.Agent) {
	t.Helper()
	got := ingLive(t, rd)
	switch {
	case len(g.order) != 0:
		t.Fatal("the controller called the gateway for a running run")
	case before == nil || got == nil || got.ResourceVersion != before.ResourceVersion ||
		got.Status.Phase != corev1alpha1.AgentRunning || !slices.Contains(got.Finalizers, finalizer):
		t.Fatal("the running run did not continue")
	case !ingHas(t, rd, &corev1.Secret{}) || !ingHas(t, rd, &batchv1.Job{}):
		t.Fatal("the controller deleted a run object")
	}
}

// ingCache returns a client that returns stale for each Agent read, as an old cache can. It counts
// these reads in reads.
func ingCache(live client.WithWatch, stale *corev1alpha1.Agent, reads *int) client.Client {
	return interceptor.NewClient(live, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch,
		key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if a, ok := obj.(*corev1alpha1.Agent); ok {
			*reads++
			stale.DeepCopyInto(a)
			return nil
		}
		return c.Get(ctx, key, obj, opts...)
	}})
}

// ingJobCache returns a client that does not find a Job, as a cache that is not current can. It
// counts these reads in reads.
func ingJobCache(live client.WithWatch, reads *int) client.Client {
	return interceptor.NewClient(live, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch,
		key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*batchv1.Job); ok {
			*reads++
			return apierrors.NewNotFound(batchv1.Resource("jobs"), key.Name)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
}

func TestLiveStateRunningIsNotProvisionedAgain(t *testing.T) {
	a := ingAgent()
	sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
	live := ingWorld(t, a, sec, ingJob(a))
	before := ingLive(t, live)
	// The cache has the Agent from before the run started.
	stale := before.DeepCopy()
	stale.Status.Phase, stale.Status.Job = corev1alpha1.AgentPending, ""
	g, reads := &countGateway{live: live}, 0
	ingReconcile(t, ingReconciler(ingCache(live, stale, &reads), live, g))
	if reads != 0 {
		t.Fatal("the controller read the Agent from the cache")
	}
	ingExpectStillRunning(t, live, g, before)
}

func TestLiveStateRevokeUsesLiveKeyHash(t *testing.T) {
	a := ingAgent()
	sec := ingEnroll(t, a, corev1alpha1.AgentFailed)
	hash := a.Status.KeyHash
	live := ingWorld(t, a, sec)
	// The cache has the same Agent with a different key hash.
	stale := ingLive(t, live)
	ingEnroll(t, stale, corev1alpha1.AgentFailed)
	if stale.Status.KeyHash == hash {
		t.Fatal("the test made the same key two times")
	}
	g, reads := &countGateway{live: live}, 0
	ingReconcile(t, ingReconciler(ingCache(live, stale, &reads), live, g))
	got := ingLive(t, live)
	switch {
	case reads != 0:
		t.Fatal("the controller read the Agent from the cache")
	case g.creates != 0 || !slices.Equal(g.blocked, []string{hash}) || !slices.Equal(g.order, []string{"block", "usage"}):
		t.Fatal("the revoke did not block the live key hash first, or it made a key")
	case got == nil || got.Status.Phase != corev1alpha1.AgentFailed || slices.Contains(got.Finalizers, finalizer) ||
		!meta.IsStatusConditionTrue(got.Status.Conditions, condRevoked) || ingHas(t, live, &corev1.Secret{}):
		t.Fatal("the revoke did not complete")
	}
}

func TestLiveStateProvisionsLiveUID(t *testing.T) {
	// The cache has the run of an old Agent with the same name.
	stale := ingAgent()
	stale.UID = "uid-old"
	ingEnroll(t, stale, corev1alpha1.AgentRunning)
	live := ingWorld(t, ingAgent())
	g, reads := &countGateway{live: live}, 0
	ingReconcile(t, ingReconciler(ingCache(live, stale, &reads), live, g))
	if reads != 0 {
		t.Fatal("the controller read the Agent from the cache")
	}
	ingExpectRunning(t, live, g)
	a, job := ingLive(t, live), &batchv1.Job{}
	if a.UID == stale.UID || a.Status.KeyAlias != "kd-"+string(a.UID) || !ingHas(t, live, job) ||
		!metav1.IsControlledBy(job, a) {
		t.Fatal("the run is not for the UID of the live Agent")
	}
}

func TestLiveStateDeletedAgentIgnoresCache(t *testing.T) {
	stale := ingAgent()
	ingEnroll(t, stale, corev1alpha1.AgentRunning)
	live := ingWorld(t)
	g, reads := &countGateway{live: live}, 0
	ingReconcile(t, ingReconciler(ingCache(live, stale, &reads), live, g))
	if len(g.order) != 0 || reads != 0 || ingHas(t, live, &corev1.Secret{}) {
		t.Fatal("the controller used the cached Agent after the delete")
	}
	ingNoAccess(t, live)
}

func TestLiveStateRunningIgnoresStaleJobCache(t *testing.T) {
	a := ingAgent()
	sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
	live := ingWorld(t, a, sec, ingJob(a))
	before := ingLive(t, live)
	g, reads := &countGateway{live: live}, 0
	ingReconcile(t, ingReconciler(ingJobCache(live, &reads), live, g))
	if reads != 0 {
		t.Fatal("the controller read the run Job from the cache")
	}
	ingExpectStillRunning(t, live, g, before)
}

func TestLiveStateNoAPIReader(t *testing.T) {
	live := ingWorld(t, ingAgent())
	g := &countGateway{live: live}
	_, err := ingReconciler(live, nil, g).Reconcile(t.Context(), reconcile.Request{NamespacedName: ingKey})
	a := ingLive(t, live)
	switch {
	case !errors.Is(err, errNoAPIReader):
		t.Fatal("the controller did not stop with no API reader")
	case len(g.order) != 0 || a == nil || len(a.Finalizers) != 0 || a.Status.Phase != "" ||
		a.Status.KeyHash != "" || len(a.Status.Conditions) > 0:
		t.Fatal("the controller changed the Agent with no API reader")
	case ingHas(t, live, &corev1.Secret{}):
		t.Fatal("the controller made a Secret with no API reader")
	}
	ingNoAccess(t, live)
}
