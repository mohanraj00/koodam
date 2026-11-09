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
	"context"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

func TestResolveTarget(t *testing.T) {
	f := newTargetWorld(t.Context())
	r := tgResolver(t, f)
	before := map[string]client.Object{}
	for k, o := range f.objs {
		before[k] = o.DeepCopyObject().(client.Object)
	}
	got, err := r.resolve(f.ctx, tgNamespace, tgAgentName)
	if err != nil || got != tgWant() {
		t.Fatal("resolve did not return the exact target")
	}
	if !readsAre(f.ops, len(resolveReads)) || f.otherCtx || f.badQuery {
		t.Fatal("resolve did not make the exact reads and the exact pod query with the context")
	}
	for k, o := range f.objs {
		if !equality.Semantic.DeepEqual(before[k], o) {
			t.Fatal("resolve changed a stored object")
		}
	}
}

func TestResolveTargetInput(t *testing.T) {
	long := strings.Repeat("a", 254)
	for _, tc := range []struct {
		name, namespace, agent string
		want                   error
	}{
		{litCaseEmptyNS, "", tgAgentName, ErrRoute},
		{litCaseUpperNS, tgUpperNamespace, tgAgentName, ErrRoute},
		{"namespace with slash", "runs/x", tgAgentName, ErrRoute},
		{"namespace with dot", "runs.x", tgAgentName, ErrRoute},
		{litCaseLongNS, strings.Repeat("a", 64), tgAgentName, ErrRoute},
		{"empty agent", tgNamespace, "", ErrRoute},
		{"upper case agent", tgNamespace, "Web", ErrRoute},
		{"agent with underscore", tgNamespace, "a_b", ErrRoute},
		{"agent with slash", tgNamespace, "a/b", ErrRoute},
		{"agent with dot segment", tgNamespace, "a..b", ErrRoute},
		{"long agent", tgNamespace, long, ErrRoute},
		{"unconfigured namespace", tgOther, tgAgentName, ErrTargetAbsent},
		{"proxy namespace", tgProxyNamespace, tgAgentName, ErrTargetAbsent},
		{"default namespace", "default", tgAgentName, ErrTargetAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			got, err := tgResolver(t, f).resolve(f.ctx, tc.namespace, tc.agent)
			refusedTarget(t, got, err, tc.want)
			if len(f.ops) != 0 {
				t.Fatal("resolve read an object for a refused route")
			}
		})
	}
	var nilResolver *targetResolver
	got, err := nilResolver.resolve(t.Context(), tgNamespace, tgAgentName)
	refusedTarget(t, got, err, ErrTargetUnavailable)
}

// TestResolveTargetFirstLookup proves that resolve returns the fixed error of the first lookup, and
// that it makes no later read.
func TestResolveTargetFirstLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*targetReader)
		want   error
		reads  int
	}{
		{"first agent read absent", func(f *targetReader) { f.remove(agentKind, tgNamespace, tgAgentName) }, ErrTargetAbsent, 1},
		{"first agent not opted in", func(f *targetReader) { f.agent().Spec.IngressPort = nil }, ErrTargetAbsent, 1},
		{"first job read absent", func(f *targetReader) { f.remove(jobKind, tgNamespace, tgJobName) }, ErrTargetAbsent, 2},
		{"first pod list empty", func(f *targetReader) { f.remove(podKind, tgNamespace, tgPodName) }, ErrTargetUnavailable, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			r := tgResolver(t, f)
			tc.change(f)
			got, err := r.resolve(f.ctx, tgNamespace, tgAgentName)
			refusedTarget(t, got, err, tc.want)
			if !readsAre(f.ops, tc.reads) {
				t.Fatal("resolve made a read after the first lookup refused")
			}
		})
	}
}

// TestResolveTargetNamespacePolicy proves that resolve checks the policy of the route namespace,
// not the policy of a different run namespace.
func TestResolveTargetNamespacePolicy(t *testing.T) {
	f := newTargetWorld(t.Context())
	r := tgResolver(t, f)
	f.listNamespace, f.listAgent, f.listRun = tgOtherNamespace, tgOther, tgOtherJob
	f.put(tgOtherRun(tgOtherNamespace, tgAgentUID, tgJobUID, tgPodUID)...)
	want := tgWant()
	want.namespace, want.agent, want.job, want.pod = tgOtherNamespace, tgOther, tgOtherJob, tgOtherPod

	f.remove(policyKind, tgNamespace, tgPolicyName)
	got, err := r.resolve(f.ctx, tgOtherNamespace, tgOther)
	if err != nil || got != want || f.badQuery {
		t.Fatal("resolve did not use the policy and the pod query of the route namespace")
	}
	f.put(tgPolicy(tgNamespace))
	f.remove(policyKind, tgOtherNamespace, tgPolicyName)
	got, err = r.resolve(f.ctx, tgOtherNamespace, tgOther)
	refusedTarget(t, got, err, ErrTargetUnavailable)
}

// TestResolveTargetRecheck changes the fake world before one read of the recheck. Each change
// must give an empty target and ErrTargetUnavailable, also a change that removes an object.
func TestResolveTargetRecheck(t *testing.T) {
	const (
		atAgent  = finalRead
		atJob    = finalRead + 1
		atList   = finalRead + 2
		atPod    = finalRead + 3
		atPolicy = finalRead + 4
	)
	// replaceRun moves the Agent to a second valid run. Each object passes its own checks, thus
	// only the compare with the first target refuses. The recheck lists the pods of the second
	// run, thus the exact pod query of the fake also changes.
	replaceRun := func(f *targetReader) {
		f.agent().Status.Job = tgNextJob
		f.listRun = tgNextJob
		j := tgJob()
		j.Name, j.UID = tgNextJob, tgOtherUID
		f.put(j)
		p := tgPod(tgNextJob+"-a1b2c", tgSecondPodUID)
		p.Labels[targetRunLabel] = tgNextJob
		p.OwnerReferences[0].Name, p.OwnerReferences[0].UID = tgNextJob, tgOtherUID
		f.put(p)
	}
	for _, tc := range []struct {
		name   string
		at     int
		change func(*targetReader)
	}{
		{"agent uid", atAgent, func(f *targetReader) { f.agent().UID = tgOtherUID }},
		{"agent replaced with same owner chain", atAgent, func(f *targetReader) {
			f.agent().UID = tgOtherUID
			f.job().OwnerReferences[0].UID = tgOtherUID
		}},
		{"agent port", atAgent, func(f *targetReader) { f.agent().Spec.IngressPort = new(int32(9090)) }},
		{"agent and pod port", atAgent, func(f *targetReader) {
			f.agent().Spec.IngressPort = new(int32(9090))
			mainPort(f).ContainerPort = 9090
		}},
		{"agent not opted in", atAgent, func(f *targetReader) { f.agent().Spec.IngressPort = nil }},
		{"agent status job missing", atAgent, func(f *targetReader) { f.agent().Status.Job = tgNextJob }},
		{"agent status job empty", atAgent, func(f *targetReader) { f.agent().Status.Job = "" }},
		{"agent run replaced", atAgent, replaceRun},
		{"agent phase", atAgent, func(f *targetReader) { f.agent().Status.Phase = corev1alpha1.AgentSucceeded }},
		{"agent deleting", atAgent, func(f *targetReader) { f.agent().DeletionTimestamp = tgDeleting() }},
		{"agent removed", atAgent, func(f *targetReader) { f.remove(agentKind, tgNamespace, tgAgentName) }},
		{"second ready pod early", atAgent, func(f *targetReader) { f.put(tgPod(tgSecondPodName, tgSecondPodUID)) }},
		{"job uid", atJob, func(f *targetReader) { f.job().UID = tgOtherUID }},
		{"job replaced with same pod owner", atJob, func(f *targetReader) {
			f.job().UID = tgOtherUID
			f.pod().OwnerReferences[0].UID = tgOtherUID
		}},
		{"job owner", atJob, func(f *targetReader) { f.job().OwnerReferences[0].UID = tgOtherUID }},
		{"job complete", atJob, func(f *targetReader) {
			f.job().Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
		}},
		{"job failed", atJob, func(f *targetReader) {
			f.job().Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
		}},
		{"job deleting", atJob, func(f *targetReader) { f.job().DeletionTimestamp = tgDeleting() }},
		{"job removed", atJob, func(f *targetReader) { f.remove(jobKind, tgNamespace, tgJobName) }},
		{"second ready pod", atList, func(f *targetReader) { f.put(tgPod(tgSecondPodName, tgSecondPodUID)) }},
		{"pod replaced with same name", atList, func(f *targetReader) { f.pod().UID = tgOtherUID }},
		{"pod replaced with other name", atList, func(f *targetReader) {
			f.remove(podKind, tgNamespace, tgPodName)
			f.put(tgPod(tgSecondPodName, tgSecondPodUID))
		}},
		{"pod ip in list", atList, setIP(tgNewPodIP)},
		{"pod labels in list", atList, func(f *targetReader) { delete(f.pod().Labels, targetRunLabel) }},
		{"pod readiness in list", atList, func(f *targetReader) { f.pod().Status.Conditions = nil }},
		{"pod deleting in list", atList, func(f *targetReader) { f.pod().DeletionTimestamp = tgDeleting() }},
		{"pod uid", atPod, func(f *targetReader) { f.pod().UID = tgOtherUID }},
		{"pod ip", atPod, setIP(tgNewPodIP)},
		{"pod ip not unicast", atPod, setIP("127.0.0.1")},
		{"pod owner", atPod, func(f *targetReader) { f.pod().OwnerReferences[0].UID = tgOtherUID }},
		{"pod agent label", atPod, func(f *targetReader) { delete(f.pod().Labels, targetAgentLabel) }},
		{"pod run label", atPod, func(f *targetReader) { f.pod().Labels[targetRunLabel] = tgOtherJob }},
		{"pod port", atPod, func(f *targetReader) { mainPort(f).ContainerPort = 9090 }},
		{"pod second http port", atPod, func(f *targetReader) { f.pod().Spec.InitContainers = initWith(httpPort()) }},
		{"pod host network", atPod, func(f *targetReader) { f.pod().Spec.HostNetwork = true }},
		{"pod readiness", atPod, func(f *targetReader) { f.pod().Status.Conditions = nil }},
		{"pod phase", atPod, func(f *targetReader) { f.pod().Status.Phase = corev1.PodFailed }},
		{"pod deleting", atPod, func(f *targetReader) { f.pod().DeletionTimestamp = tgDeleting() }},
		{"pod removed", atPod, func(f *targetReader) { f.remove(podKind, tgNamespace, tgPodName) }},
		{"policy ingress drift", atPolicy, func(f *targetReader) {
			f.policy().Spec.Ingress[0].Ports[0].Port = new(intstr.FromInt32(tgPort))
		}},
		{"policy extra peer", atPolicy, func(f *targetReader) {
			f.policy().Spec.Ingress[0].From = append(f.policy().Spec.Ingress[0].From,
				networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}})
		}},
		{"policy deleting", atPolicy, func(f *targetReader) { f.policy().DeletionTimestamp = tgDeleting() }},
		{"policy removed", atPolicy, func(f *targetReader) {
			f.remove(policyKind, tgNamespace, tgPolicyName)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			r := tgResolver(t, f)
			f.before = func(i int) {
				if i == tc.at {
					tc.change(f)
				}
			}
			got, err := r.resolve(f.ctx, tgNamespace, tgAgentName)
			refusedTarget(t, got, err, ErrTargetUnavailable)
			if len(f.ops) <= tc.at || f.otherCtx || f.badQuery {
				t.Fatal("the change was not before a recheck read, or a read was not exact")
			}
			if !readsAre(f.ops, len(f.ops)) {
				t.Fatal("resolve made a read after the refusal")
			}
		})
	}
}

// TestResolveTargetReadError proves that an error of each read and a partial pod list in the
// recheck give only the fixed error, and that resolve makes no later read.
func TestResolveTargetReadError(t *testing.T) {
	for i := range resolveReads {
		f := newTargetWorld(t.Context())
		f.fail[i] = errors.New(targetCanary)
		got, err := tgResolver(t, f).resolve(f.ctx, tgNamespace, tgAgentName)
		refusedTarget(t, got, err, ErrTargetUnavailable)
		if !readsAre(f.ops, i+1) {
			t.Fatalf("resolve made a read after the API error of read %d", i)
		}
	}
	f := newTargetWorld(t.Context())
	f.listMeta[finalRead+2] = metav1.ListMeta{Continue: targetCanary}
	got, err := tgResolver(t, f).resolve(f.ctx, tgNamespace, tgAgentName)
	refusedTarget(t, got, err, ErrTargetUnavailable)
	if !readsAre(f.ops, finalRead+3) {
		t.Fatal("resolve made a read after a partial pod list in the recheck")
	}
}

// TestResolveTargetCancel ends the context before the first read and during each read. The fake
// read passes, thus resolve must check the context and make no later read.
func TestResolveTargetCancel(t *testing.T) {
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	f := newTargetWorld(ended)
	got, err := tgResolver(t, f).resolve(ended, tgNamespace, tgAgentName)
	refusedTarget(t, got, err, ErrTargetUnavailable)
	if len(f.ops) != 0 {
		t.Fatal("resolve read an object after the context ended")
	}
	for i := range resolveReads {
		during, stop := context.WithCancel(t.Context())
		defer stop()
		f := newTargetWorld(during)
		f.before = func(j int) {
			if j == i {
				stop()
			}
		}
		got, err := tgResolver(t, f).resolve(during, tgNamespace, tgAgentName)
		refusedTarget(t, got, err, ErrTargetUnavailable)
		if !readsAre(f.ops, i+1) || f.otherCtx {
			t.Fatalf("resolve made a read after the context ended during read %d", i)
		}
	}
}
