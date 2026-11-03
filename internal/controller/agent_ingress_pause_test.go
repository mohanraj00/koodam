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

// These tests are for the ingress pause and the port check of the Agent controller part of #147.
// They use the fake API, the counting gateway and the helpers of agent_live_state_test.go. They use
// no cluster, no real gateway and no real key. Each failure message is fixed text.

import (
	"errors"
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// These are the fixed fixture strings that the ingress pause tests use more than one time.
const (
	pauseTestSide          = "side"
	pauseTestInit          = "setup"
	pauseTestLiveUID       = "uid-live"
	pauseTestOldUID        = "uid-old"
	pauseTestBlock         = "block"
	pauseTestUsage         = "usage"
	pauseTestNote          = "note"
	pauseTestUser          = "user"
	pauseTestInvalidReason = "Invalid"
)

// ingAgentWith returns an Agent with the ingress port and the pod template. An Agent with a nil port
// is an ordinary Agent.
func ingAgentWith(port *int32, tmpl *corev1alpha1.PodTemplate) *corev1alpha1.Agent {
	a := ingAgent()
	a.Spec.IngressPort, a.Spec.PodTemplate = port, tmpl
	return a
}

func ingIngressReconciler(c client.Client, rd client.Reader, g *countGateway, enabled bool) *AgentReconciler {
	r := ingReconciler(c, rd, g)
	r.IngressEnabled = enabled
	return r
}

// ingExpectPaused stops the test if the controller called the gateway, changed the live Agent or
// changed the run objects. hasFinalizer and hasSecret tell if the user objects are there.
func ingExpectPaused(t *testing.T, rd client.Reader, g *countGateway, hasFinalizer, hasSecret bool) {
	t.Helper()
	a := ingLive(t, rd)
	switch {
	case len(g.order) != 0:
		t.Fatal("the paused Agent called the gateway")
	case a == nil:
		t.Fatal("the paused Agent is gone")
	case slices.Contains(a.Finalizers, finalizer) != hasFinalizer:
		t.Fatal("the pause changed the finalizers")
	case a.Status.Phase != "" || a.Status.KeyHash != "" || len(a.Status.Conditions) > 0:
		t.Fatal("the pause wrote the status")
	case ingHas(t, rd, &corev1.Secret{}) != hasSecret:
		t.Fatal("the pause changed the run Secret")
	}
	ingNoAccess(t, rd)
}

// ingExpectRevoked stops the test if the revoke did not block the stored hash first, or did not
// complete. A nil live Agent is permitted only if gone is true.
func ingExpectRevoked(t *testing.T, rd client.Reader, g *countGateway, hash string, phase corev1alpha1.AgentPhase,
	gone bool) {
	t.Helper()
	a := ingLive(t, rd)
	switch {
	case g.creates != 0 || len(g.blocked) != 1 || g.blocked[0] != hash ||
		!slices.Equal(g.order, []string{pauseTestBlock, pauseTestUsage}):
		t.Fatal("the revoke did not block the stored key hash first, or it made a key")
	case ingHas(t, rd, &corev1.Secret{}):
		t.Fatal("the revoke did not delete the run Secret")
	case (a == nil) != gone:
		t.Fatal("the Agent is not in the expected state after the revoke")
	case a != nil && (a.Status.Phase != phase || slices.Contains(a.Finalizers, finalizer) ||
		!meta.IsStatusConditionTrue(a.Status.Conditions, condRevoked)):
		t.Fatal("the revoke did not complete")
	}
}

// ingPort returns a container with one port.
func ingPort(name, port string) corev1.Container {
	return corev1.Container{Name: name, Image: testAgentImage, Ports: []corev1.ContainerPort{{Name: port, ContainerPort: 8080}}}
}

func ingTemplate(cs, inits []corev1.Container) *corev1alpha1.PodTemplate {
	return &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: cs, InitContainers: inits}}
}

func TestIngressDisabledPausesNewRun(t *testing.T) {
	for _, tc := range []struct {
		name              string
		port              int32
		finalizer, secret bool
	}{
		{"no state", 8080, false, false},
		{"user finalizer only", 8080, true, false},
		{"owned Secret only", 8080, false, true},
		{"finalizer and owned Secret", 8080, true, true},
		{"port out of range", 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ingAgentWith(new(tc.port), nil)
			objs := []client.Object{a}
			if tc.finalizer {
				a.Finalizers = []string{finalizer}
			}
			if tc.secret {
				// A Secret with the run name and the Agent as owner, but with no key.
				objs = append(objs, &corev1.Secret{ObjectMeta: runMeta(a), Data: map[string][]byte{pauseTestNote: []byte(pauseTestUser)}})
			}
			live := ingWorld(t, objs...)
			g := &countGateway{live: live}
			r := ingIngressReconciler(live, live, g, false)
			ingReconcile(t, r)
			ingReconcile(t, r)
			ingExpectPaused(t, live, g, tc.finalizer, tc.secret)
		})
	}
}

func TestIngressProvision(t *testing.T) {
	web := ingTemplate([]corev1.Container{ingPort(pauseTestSide, "web")}, nil)
	http := ingTemplate([]corev1.Container{ingPort(pauseTestSide, agentingress.ReservedPortName)},
		[]corev1.Container{ingPort(pauseTestInit, agentingress.ReservedPortName)})
	for _, tc := range []struct {
		name    string
		port    *int32
		tmpl    *corev1alpha1.PodTemplate
		enabled bool
	}{
		{"opted in with the ingress enabled", new(int32(8080)), web, true},
		{"ordinary with the ingress disabled", nil, nil, false},
		{"ordinary with the ingress enabled", nil, nil, true},
		{"ordinary port name http with the ingress disabled", nil, http, false},
		{"ordinary port name http with the ingress enabled", nil, http, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := ingWorld(t, ingAgentWith(tc.port, tc.tmpl))
			g := &countGateway{live: live}
			ingReconcile(t, ingIngressReconciler(live, live, g, tc.enabled))
			ingExpectRunning(t, live, g)
		})
	}

	t.Run("enrolled run with the ingress disabled", func(t *testing.T) {
		a := ingAgentWith(new(int32(8080)), nil)
		sec := ingEnroll(t, a, corev1alpha1.AgentPending)
		live := ingWorld(t, a, sec)
		g := &countGateway{live: live}
		ingReconcile(t, ingIngressReconciler(live, live, g, false))
		ingExpectRunning(t, live, g)
	})
}

func TestIngressRefusesBeforeKey(t *testing.T) {
	http := agentingress.ReservedPortName
	for _, tc := range []struct {
		name string
		port int32
		tmpl *corev1alpha1.PodTemplate
		want error
	}{
		{"main container http", 8080, ingTemplate([]corev1.Container{ingPort(agentContainer, http)}, nil),
			agentingress.ErrReservedPort},
		{"init container http", 8080, ingTemplate(nil, []corev1.Container{ingPort(pauseTestInit, http)}),
			agentingress.ErrReservedPort},
		{"sidecar http", 8080, ingTemplate([]corev1.Container{ingPort(pauseTestSide, http)}, nil), agentingress.ErrReservedPort},
		{"port 0", 0, nil, agentingress.ErrPortRange},
		{"negative port", -1, nil, agentingress.ErrPortRange},
		{"port 65536", 65536, nil, agentingress.ErrPortRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := ingWorld(t, ingAgentWith(new(tc.port), tc.tmpl))
			g := &countGateway{live: live}
			ingReconcile(t, ingIngressReconciler(live, live, g, true))
			a := ingLive(t, live)
			if a == nil {
				t.Fatal("the Agent is gone")
			}
			c := meta.FindStatusCondition(a.Status.Conditions, condProvisioned)
			switch {
			case len(g.order) != 0:
				t.Fatal("the controller called the gateway for an Agent that is not valid")
			case a.Status.Phase != corev1alpha1.AgentFailed || c == nil || c.Reason != pauseTestInvalidReason ||
				c.Message != tc.want.Error():
				t.Fatal("the Agent does not have the fixed Invalid status")
			case slices.Contains(a.Finalizers, finalizer) || ingHas(t, live, &corev1.Secret{}):
				t.Fatal("the controller made a finalizer or a Secret")
			}
			ingNoAccess(t, live)
		})
	}
}

func TestIngressDisabledRevokes(t *testing.T) {
	mainHTTP := ingTemplate([]corev1.Container{ingPort(agentContainer, agentingress.ReservedPortName)}, nil)
	for _, tc := range []struct {
		name string
		port int32
		tmpl *corev1alpha1.PodTemplate
	}{
		{"enrolled run with port 0", 0, nil},
		{"enrolled run with main container http", 8080, mainHTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ingAgentWith(new(tc.port), tc.tmpl)
			sec := ingEnroll(t, a, corev1alpha1.AgentPending)
			hash := a.Status.KeyHash
			live := ingWorld(t, a, sec)
			g := &countGateway{live: live}
			ingReconcile(t, ingIngressReconciler(live, live, g, false))
			ingExpectRevoked(t, live, g, hash, corev1alpha1.AgentFailed, false)
		})
	}

	t.Run("run end", func(t *testing.T) {
		a := ingAgentWith(new(int32(8080)), nil)
		sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
		hash := a.Status.KeyHash
		job := ingJob(a)
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		live := ingWorld(t, a, sec, job)
		g := &countGateway{live: live}
		ingReconcile(t, ingIngressReconciler(live, live, g, false))
		ingExpectRevoked(t, live, g, hash, corev1alpha1.AgentSucceeded, false)
		if !ingHas(t, live, &batchv1.Job{}) {
			t.Fatal("the revoke deleted the Job of a run that ended")
		}
	})

	t.Run("delete with port 0", func(t *testing.T) {
		a := ingAgentWith(new(int32(0)), nil)
		sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
		hash := a.Status.KeyHash
		live := ingWorld(t, a, sec, ingJob(a))
		if live.Delete(t.Context(), ingAgent()) != nil {
			t.Fatal("the test could not delete the Agent")
		}
		g := &countGateway{live: live}
		ingReconcile(t, ingIngressReconciler(live, live, g, false))
		ingExpectRevoked(t, live, g, hash, "", true)
		ingNoAccess(t, live)
	})

	t.Run("running run continues", func(t *testing.T) {
		a := ingAgentWith(new(int32(8080)), nil)
		sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
		live := ingWorld(t, a, sec, ingJob(a))
		before := ingLive(t, live)
		g := &countGateway{live: live}
		ingReconcile(t, ingIngressReconciler(live, live, g, false))
		ingExpectStillRunning(t, live, g, before)
	})
}

func TestIngressReadsLiveAgent(t *testing.T) {
	port := new(int32(8080))
	enrolled := func(uid types.UID, phase corev1alpha1.AgentPhase) (*corev1alpha1.Agent, *corev1.Secret) {
		a := ingAgentWith(port, nil)
		a.UID = uid
		return a, ingEnroll(t, a, phase)
	}

	t.Run("live key hash and cached no key hash", func(t *testing.T) {
		a, sec := enrolled(pauseTestLiveUID, corev1alpha1.AgentPending)
		live := ingWorld(t, a, sec)
		g, reads := &countGateway{live: live}, 0
		ingReconcile(t, ingIngressReconciler(ingCache(live, ingAgentWith(port, nil), &reads), live, g, false))
		ingExpectRunning(t, live, g)
		if reads != 0 {
			t.Fatal("the controller read the Agent from the cache")
		}
	})

	for _, tc := range []struct {
		name  string
		uid   types.UID
		phase corev1alpha1.AgentPhase
	}{
		{"live no key hash and cached key hash", pauseTestLiveUID, corev1alpha1.AgentPending},
		{"live Agent replaced and cached old UID", pauseTestOldUID, corev1alpha1.AgentRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale, _ := enrolled(tc.uid, tc.phase)
			live := ingWorld(t, ingAgentWith(port, nil))
			g, reads := &countGateway{live: live}, 0
			ingReconcile(t, ingIngressReconciler(ingCache(live, stale, &reads), live, g, false))
			ingExpectPaused(t, live, g, false, false)
			if reads != 0 {
				t.Fatal("the controller read the Agent from the cache")
			}
		})
	}

	t.Run("live Agent deleted and cached run", func(t *testing.T) {
		stale, _ := enrolled(pauseTestOldUID, corev1alpha1.AgentRunning)
		live := ingWorld(t)
		g, reads := &countGateway{live: live}, 0
		ingReconcile(t, ingIngressReconciler(ingCache(live, stale, &reads), live, g, true))
		if len(g.order) != 0 || reads != 0 || ingHas(t, live, &corev1.Secret{}) {
			t.Fatal("the controller used the cached Agent after the delete")
		}
		ingNoAccess(t, live)
	})

	t.Run("no API reader", func(t *testing.T) {
		live := ingWorld(t, ingAgentWith(port, nil))
		g := &countGateway{live: live}
		_, err := ingIngressReconciler(live, nil, g, true).Reconcile(t.Context(), reconcile.Request{NamespacedName: ingKey})
		if !errors.Is(err, errNoAPIReader) {
			t.Fatal("the controller did not stop with no API reader")
		}
		ingExpectPaused(t, live, g, false, false)
	})
}

func TestIngressRunningWithoutEnrollmentPauses(t *testing.T) {
	a := ingAgentWith(new(int32(8080)), nil)
	a.Finalizers = []string{finalizer}
	a.Status = corev1alpha1.AgentStatus{Phase: corev1alpha1.AgentRunning}
	// A Secret with the run name and the Agent as owner, but with no key. There is no Job.
	sec := &corev1.Secret{ObjectMeta: runMeta(a), Data: map[string][]byte{pauseTestNote: []byte(pauseTestUser)}}
	live := ingWorld(t, a, sec)
	before := ingLive(t, live)
	g := &countGateway{live: live}
	r := ingIngressReconciler(live, live, g, false)
	ingReconcile(t, r)
	ingReconcile(t, r)
	got := ingLive(t, live)
	switch {
	case len(g.order) != 0:
		t.Fatal("the paused Agent called the gateway")
	case before == nil || got == nil:
		t.Fatal("the paused Agent is gone")
	case got.ResourceVersion != before.ResourceVersion:
		t.Fatal("the pause wrote the Agent or its status")
	case got.Status.Phase != corev1alpha1.AgentRunning || got.Status.KeyHash != "" ||
		len(got.Status.Conditions) > 0 || !slices.Contains(got.Finalizers, finalizer):
		t.Fatal("the pause changed the status or the finalizers")
	case !ingHas(t, live, &corev1.Secret{}):
		t.Fatal("the pause deleted the run Secret")
	}
	ingNoAccess(t, live)
}

func TestIngressRunningIgnoresStaleJobCache(t *testing.T) {
	// TestLiveStateRunningIgnoresStaleJobCache has the ordinary Agent with the ingress disabled.
	for _, tc := range []struct {
		name    string
		port    *int32
		enabled bool
	}{
		{"opted in with the ingress disabled", new(int32(8080)), false},
		{"opted in with the ingress enabled", new(int32(8080)), true},
		{"ordinary with the ingress enabled", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ingAgentWith(tc.port, nil)
			sec := ingEnroll(t, a, corev1alpha1.AgentRunning)
			live := ingWorld(t, a, sec, ingJob(a))
			before := ingLive(t, live)
			g, reads := &countGateway{live: live}, 0
			ingReconcile(t, ingIngressReconciler(ingJobCache(live, &reads), live, g, tc.enabled))
			if reads != 0 {
				t.Fatal("the controller read the run Job from the cache")
			}
			ingExpectStillRunning(t, live, g, before)
		})
	}
}
