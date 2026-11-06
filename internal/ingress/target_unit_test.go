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

package ingress

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The tests in this file call one part of the resolver directly, not through resolve.

// TestTargetCheckAgent calls checkAgent on a valid Agent and on one change for each refusal
// class. Only an Agent with no ingress port is absent, also when it is deleting. A refusal returns
// no port and no Job name.
func TestTargetCheckAgent(t *testing.T) {
	port, job, err := checkAgent(tgAgent(), tgNamespace, tgAgentName)
	if err != nil || port != tgPort || job != tgJobName {
		t.Fatal("checkAgent refused a valid Agent")
	}
	for _, tc := range []struct {
		name   string
		change func(*corev1alpha1.Agent)
		want   error
	}{
		{"no ingress port", func(a *corev1alpha1.Agent) { a.Spec.IngressPort = nil }, ErrTargetAbsent},
		{"no ingress port and deletion", func(a *corev1alpha1.Agent) {
			a.Spec.IngressPort = nil
			a.DeletionTimestamp = tgDeleting()
		}, ErrTargetAbsent},
		{"empty status job", func(a *corev1alpha1.Agent) { a.Status.Job = "" }, ErrTargetUnavailable},
		{"status job not a label value", func(a *corev1alpha1.Agent) { a.Status.Job = "Bad_Job" }, ErrTargetUnavailable},
		{"pending phase", func(a *corev1alpha1.Agent) { a.Status.Phase = corev1alpha1.AgentPending }, ErrTargetUnavailable},
		{"agent deletion", func(a *corev1alpha1.Agent) { a.DeletionTimestamp = tgDeleting() }, ErrTargetUnavailable},
		{"no agent uid", func(a *corev1alpha1.Agent) { a.UID = "" }, ErrTargetUnavailable},
		{"ingress port out of range", func(a *corev1alpha1.Agent) { a.Spec.IngressPort = new(int32(0)) },
			ErrTargetUnavailable},
		{"agent in other namespace", func(a *corev1alpha1.Agent) { a.Namespace = tgOtherNamespace }, ErrTargetUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tgAgent()
			tc.change(a)
			port, job, err := checkAgent(a, tgNamespace, tgAgentName)
			if err != tc.want || port != 0 || job != "" {
				t.Fatal("checkAgent did not refuse with only the fixed error")
			}
		})
	}
	// A valid DNS subdomain with 81 characters is not a valid label value for the pod selector.
	long := tgAgent()
	long.Name = strings.Repeat("a", 40) + "." + strings.Repeat("b", 40)
	if _, _, err := checkAgent(long, tgNamespace, long.Name); err != ErrTargetUnavailable {
		t.Fatal("checkAgent accepted an Agent name that is not a valid label value")
	}
}

// TestTargetCheckJob calls checkJob with the run Job and the partial target of the first lookup.
// The sole controller owner must be the exact Agent, and only a true end condition ends the Job.
func TestTargetCheckJob(t *testing.T) {
	end := func(c batchv1.JobConditionType, s corev1.ConditionStatus) func(*batchv1.Job) {
		return func(j *batchv1.Job) { j.Status.Conditions = []batchv1.JobCondition{{Type: c, Status: s}} }
	}
	for _, tc := range []struct {
		name   string
		change func(*batchv1.Job)
		want   error
	}{
		{"active", func(*batchv1.Job) {}, nil},
		{"false end condition", end(batchv1.JobComplete, corev1.ConditionFalse), nil},
		{"unknown end condition", end(batchv1.JobFailed, corev1.ConditionUnknown), nil},
		{"true complete condition", end(batchv1.JobComplete, corev1.ConditionTrue), ErrTargetUnavailable},
		{"true failed condition", end(batchv1.JobFailed, corev1.ConditionTrue), ErrTargetUnavailable},
		{"job deletion", func(j *batchv1.Job) { j.DeletionTimestamp = tgDeleting() }, ErrTargetUnavailable},
		{"other job name", func(j *batchv1.Job) { j.Name = tgOtherJob }, ErrTargetUnavailable},
		{"owner with other uid", func(j *batchv1.Job) { j.OwnerReferences[0].UID = tgOtherUID }, ErrTargetUnavailable},
		{"owner with no controller flag", func(j *batchv1.Job) { j.OwnerReferences[0].Controller = nil },
			ErrTargetUnavailable},
		{"second controller", func(j *batchv1.Job) {
			j.OwnerReferences = append(j.OwnerReferences, j.OwnerReferences[0])
		}, ErrTargetUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := tgJob()
			tc.change(j)
			if checkJob(j, tgWant()) != tc.want {
				t.Fatal("checkJob did not return the fixed result")
			}
		})
	}
}

// TestTargetEligiblePod calls eligiblePod with the partial target of the first lookup. Each pod of
// the run is eligible alone; the lookup must refuse two eligible pods.
func TestTargetEligiblePod(t *testing.T) {
	notReady := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}
	for _, tc := range []struct {
		name   string
		change func(*corev1.Pod)
		want   bool
	}{
		{"running and ready", func(*corev1.Pod) {}, true},
		{"second ready condition false", func(p *corev1.Pod) {
			p.Status.Conditions = append(p.Status.Conditions, notReady)
		}, false},
		{"no ready condition", func(p *corev1.Pod) { p.Status.Conditions = nil }, false},
		{"pod pending", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, false},
		{"pod deletion", func(p *corev1.Pod) { p.DeletionTimestamp = tgDeleting() }, false},
		{"no pod uid", func(p *corev1.Pod) { p.UID = "" }, false},
		{"run label of other job", func(p *corev1.Pod) { p.Labels[targetRunLabel] = tgOtherJob }, false},
		{"agent label removed", func(p *corev1.Pod) { delete(p.Labels, targetAgentLabel) }, false},
		{"pod in other namespace", func(p *corev1.Pod) { p.Namespace = tgOtherNamespace }, false},
		{"owner not a controller", func(p *corev1.Pod) { p.OwnerReferences[0].Controller = new(false) }, false},
		{"owner of other kind", func(p *corev1.Pod) { p.OwnerReferences[0].Kind = "ReplicaSet" }, false},
		{"owner with stale job uid", func(p *corev1.Pod) { p.OwnerReferences[0].UID = tgOtherUID }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tgPod(tgPodName, tgPodUID)
			tc.change(p)
			if eligiblePod(p, tgWant()) != tc.want {
				t.Fatal("eligiblePod did not return the expected result")
			}
		})
	}
	if !eligiblePod(tgPod(tgSecondPodName, tgSecondPodUID), tgWant()) {
		t.Fatal("eligiblePod refused a second ready pod of the run")
	}
}

// TestTargetCheckPoliciesUnconfigured proves that the startup check of a resolver that did not
// come from newTargetResolver refuses with ErrTargetUnavailable and reads no object. An empty
// namespace set must never pass. TestTargetCheckPoliciesCancel has the nil resolver.
func TestTargetCheckPoliciesUnconfigured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*targetReader) *targetResolver
	}{
		{"no reader and no guards", func(*targetReader) *targetResolver { return &targetResolver{} }},
		{"guards with no reader", func(*targetReader) *targetResolver {
			return &targetResolver{guards: map[string]agentingress.PolicyGuard{tgNamespace: tgGuard(tgNamespace)}}
		}},
		{"reader with nil guards", func(f *targetReader) *targetResolver { return &targetResolver{reader: f} }},
		{"reader with empty guards", func(f *targetReader) *targetResolver {
			return &targetResolver{reader: f, guards: map[string]agentingress.PolicyGuard{}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			if tc.build(f).checkPolicies(f.ctx) != ErrTargetUnavailable || len(f.ops) != 0 {
				t.Fatal("checkPolicies of an unconfigured resolver did not refuse with no read")
			}
		})
	}
}
