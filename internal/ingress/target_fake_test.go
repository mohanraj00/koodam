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
	"net/netip"
	"reflect"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The fixtures have only public fake names, UIDs, ports, images and status. They have no env,
// token, Secret, key alias or key hash.
const (
	tgNamespace      = "runs"
	tgOtherNamespace = "runs-b"
	tgAgentName      = "web"
	tgJobName        = "kd-web"
	tgPodName        = "kd-web-x7k2p"
	tgSecondPodName  = "kd-web-q4m9z"
	tgPolicyName     = "koodam-agent"
	tgProxyNamespace = "koodam-system"
	tgRelease        = "koodam"
	tgPort           = int32(8080)
	tgPodIP          = "10.244.1.7"
	tgPortSuffix     = ":8080"
	tgAddr           = tgPodIP + tgPortSuffix
	tgOther          = "other"
	tgOtherJob       = "kd-other"
	tgNameKey        = "app.kubernetes.io/name"
	tgInstanceKey    = "app.kubernetes.io/instance"
	tgComponentKey   = "app.kubernetes.io/component"

	tgAgentUID     types.UID = "00000000-0000-4000-8000-000000000001"
	tgJobUID       types.UID = "00000000-0000-4000-8000-000000000002"
	tgPodUID       types.UID = "00000000-0000-4000-8000-000000000003"
	tgSecondPodUID types.UID = "00000000-0000-4000-8000-000000000004"
	tgOtherUID     types.UID = "00000000-0000-4000-8000-000000000009"
)

// targetCanary is a fixed private detail. It is not a credential. A fake API puts it in an error.
// An error from the resolver must not contain it.
const targetCanary = "target-detail-canary"

// The fixed names of the fake reads. A failure message never contains an object, an error or an
// address.
const (
	policyKind = "NetworkPolicy"

	getPrefix   = "get "
	opGetPolicy = getPrefix + policyKind
	opOther     = "unexpected read"
)

// targetReader is a local fake client.Reader. It keeps the objects and the fixed names of the
// reads only in memory in this process. It has no write method. It does not end a read when the
// context ends, thus the resolver must check the context itself.
type targetReader struct {
	ctx      context.Context // Each read must get this context.
	objs     map[string]client.Object
	ops      []string
	otherCtx bool
	// badQuery is true if a read had an option or a list query that is not the exact run query.
	badQuery bool
	// before runs before read i, and fail gives the error of read i.
	before func(i int)
	fail   map[int]error
}

// newTargetWorld returns a fake with the guard policy in the two configured run namespaces.
func newTargetWorld(ctx context.Context) *targetReader {
	f := &targetReader{ctx: ctx, objs: map[string]client.Object{}, fail: map[int]error{}}
	f.put(tgPolicy(tgNamespace), tgPolicy(tgOtherNamespace))
	return f
}

func objKind(o runtime.Object) string {
	switch o.(type) {
	case *networkingv1.NetworkPolicy:
		return policyKind
	}
	return opOther
}

func storeKey(kind, namespace, name string) string { return kind + "/" + namespace + "/" + name }

func (f *targetReader) put(objs ...client.Object) {
	for _, o := range objs {
		f.objs[storeKey(objKind(o), o.GetNamespace(), o.GetName())] = o
	}
}

func (f *targetReader) remove(kind, namespace, name string) {
	delete(f.objs, storeKey(kind, namespace, name))
}

func (f *targetReader) policy() *networkingv1.NetworkPolicy {
	return f.objs[storeKey(policyKind, tgNamespace, tgPolicyName)].(*networkingv1.NetworkPolicy)
}

// start records the read and runs the hooks. It returns the error of the read.
func (f *targetReader) start(ctx context.Context, op string) error {
	i := len(f.ops)
	f.ops = append(f.ops, op)
	if ctx != f.ctx {
		f.otherCtx = true
	}
	if f.before != nil {
		f.before(i)
	}
	return f.fail[i]
}

func (f *targetReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	kind := objKind(obj)
	op := opOther
	if kind != opOther {
		op = getPrefix + kind
	}
	if len(opts) > 0 {
		f.badQuery = true
	}
	if err := f.start(ctx, op); err != nil {
		return err
	}
	stored, ok := f.objs[storeKey(kind, key.Namespace, key.Name)]
	if !ok || op == opOther {
		return apierrors.NewNotFound(schema.GroupResource{}, "")
	}
	reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(stored.DeepCopyObject()).Elem())
	return nil
}

// List records an unexpected read and refuses it. The startup policy check makes no list.
func (f *targetReader) List(ctx context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	f.badQuery = true
	if err := f.start(ctx, opOther); err != nil {
		return err
	}
	return apierrors.NewBadRequest("")
}

var _ client.Reader = (*targetReader)(nil)

func tgController(apiVersion, kind, name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid,
		Controller: new(true), BlockOwnerDeletion: new(true)}
}

func tgDeleting() *metav1.Time { return new(metav1.Unix(1, 0)) }

func tgAgent() *corev1alpha1.Agent {
	return &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: tgAgentName, Namespace: tgNamespace, UID: tgAgentUID},
		Spec: corev1alpha1.AgentSpec{Image: "example.test/agent:1", Command: []string{"serve"},
			Model: "test-model", Budget: resource.MustParse("1"), IngressPort: new(tgPort)},
		Status: corev1alpha1.AgentStatus{Phase: corev1alpha1.AgentRunning, Job: tgJobName},
	}
}

func tgJob() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: tgJobName, Namespace: tgNamespace, UID: tgJobUID,
			Labels: map[string]string{targetAgentLabel: tgAgentName},
			OwnerReferences: []metav1.OwnerReference{
				tgController("core.koodam.dev/v1alpha1", agentKind, tgAgentName, tgAgentUID)}},
		Status: batchv1.JobStatus{Active: 1},
	}
}

func tgPod(name string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tgNamespace, UID: uid,
			Labels: map[string]string{targetAgentLabel: tgAgentName, targetRunLabel: tgJobName,
				"batch.kubernetes.io/job-name": tgJobName},
			OwnerReferences: []metav1.OwnerReference{tgController("batch/v1", jobKind, tgJobName, tgJobUID)}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: targetContainer, Image: "example.test/agent:1",
			Ports: []corev1.ContainerPort{{Name: agentingress.ReservedPortName, ContainerPort: tgPort,
				Protocol: corev1.ProtocolTCP}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: tgPodIP,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

func tgProxyLabels() map[string]string {
	return map[string]string{tgNameKey: tgRelease, tgInstanceKey: tgRelease,
		tgComponentKey: agentingress.ProxyComponent}
}

func tgGuard(namespace string) agentingress.PolicyGuard {
	return agentingress.PolicyGuard{Namespace: namespace, Name: tgPolicyName, ProxyNamespace: tgProxyNamespace,
		ProxyLabels: tgProxyLabels()}
}

// tgPolicy returns the exact guard policy of namespace, with one egress rule.
func tgPolicy(namespace string) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: tgPolicyName, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: targetAgentLabel, Operator: metav1.LabelSelectorOpExists}}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": tgProxyNamespace}},
					PodSelector: &metav1.LabelSelector{MatchLabels: tgProxyLabels()},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromString(agentingress.ReservedPortName))}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromInt32(8080))}}}},
		},
	}
}

// tgResolver returns a resolver of f with the guards of the two run namespaces.
func tgResolver(t *testing.T, f *targetReader) *targetResolver {
	t.Helper()
	r, err := newTargetResolver(f, []agentingress.PolicyGuard{tgGuard(tgNamespace), tgGuard(tgOtherNamespace)})
	if err != nil || r == nil {
		t.Fatal("newTargetResolver refused a valid configuration")
	}
	return r
}

func tgWant() target {
	return target{namespace: tgNamespace, agent: tgAgentName, agentUID: tgAgentUID, job: tgJobName,
		jobUID: tgJobUID, pod: tgPodName, podUID: tgPodUID, addr: netip.MustParseAddrPort(tgAddr)}
}
