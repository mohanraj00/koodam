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
	"net/netip"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

// TestTargetFields proves that a target has only names, UIDs and the address. A new field with
// an API object, a spec or caller data fails this test.
func TestTargetFields(t *testing.T) {
	kinds := []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[types.UID](), reflect.TypeFor[netip.AddrPort]()}
	tt := reflect.TypeFor[target]()
	if tt.NumField() != 8 {
		t.Fatalf("a target has %d fields, want 8", tt.NumField())
	}
	for f := range tt.Fields() {
		if !slices.Contains(kinds, f.Type) {
			t.Fatal("a target field is not a name, a UID or the address")
		}
	}
}

func TestNewTargetResolver(t *testing.T) {
	f := newTargetWorld(t.Context())
	r := tgResolver(t, f)
	if len(r.guards) != 2 || r.guards[tgNamespace].Namespace != tgNamespace ||
		r.guards[tgOtherNamespace].Namespace != tgOtherNamespace {
		t.Fatal("the resolver does not have exactly the two configured run namespaces")
	}
	if len(f.ops) != 0 {
		t.Fatal("newTargetResolver read an object")
	}
}

func TestNewTargetResolverRefuses(t *testing.T) {
	g := tgGuard
	withLabel := func(k, v string) agentingress.PolicyGuard {
		x := tgGuard(tgOtherNamespace)
		x.ProxyLabels[k] = v
		return x
	}
	for _, tc := range []struct {
		name   string
		nilRdr bool
		guards []agentingress.PolicyGuard
	}{
		{"nil reader", true, []agentingress.PolicyGuard{g(tgNamespace)}},
		{"nil guards", false, nil},
		{"empty guards", false, []agentingress.PolicyGuard{}},
		{litCaseEmptyNS, false, []agentingress.PolicyGuard{g("")}},
		{"wildcard namespace", false, []agentingress.PolicyGuard{g("*")}},
		{"wildcard after a valid namespace", false, []agentingress.PolicyGuard{g(tgNamespace), g("*")}},
		{litCaseUpperNS, false, []agentingress.PolicyGuard{g("Runs")}},
		{"namespace with a dot", false, []agentingress.PolicyGuard{g("runs.a")}},
		{"duplicate namespace", false, []agentingress.PolicyGuard{g(tgNamespace), g(tgNamespace)}},
		{"invalid policy name", false, []agentingress.PolicyGuard{{Namespace: tgNamespace, Name: "Bad_Name",
			ProxyNamespace: tgProxyNamespace, ProxyLabels: tgProxyLabels()}}},
		{"invalid proxy labels", false, []agentingress.PolicyGuard{{Namespace: tgNamespace, Name: tgPolicyName,
			ProxyNamespace: tgProxyNamespace, ProxyLabels: map[string]string{"app": "x"}}}},
		{"different policy name", false, []agentingress.PolicyGuard{g(tgNamespace), {Namespace: tgOtherNamespace,
			Name: "other-agent", ProxyNamespace: tgProxyNamespace, ProxyLabels: tgProxyLabels()}}},
		{"different proxy namespace", false, []agentingress.PolicyGuard{g(tgNamespace), {Namespace: tgOtherNamespace,
			Name: tgPolicyName, ProxyNamespace: "other-system", ProxyLabels: tgProxyLabels()}}},
		{"different proxy labels", false, []agentingress.PolicyGuard{g(tgNamespace),
			withLabel(tgInstanceKey, tgOther)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			var rdr client.Reader = f
			if tc.nilRdr {
				rdr = nil
			}
			r, err := newTargetResolver(rdr, tc.guards)
			if r != nil || err != ErrTargetConfig {
				t.Fatal("newTargetResolver did not refuse with only ErrTargetConfig")
			}
			if len(f.ops) != 0 {
				t.Fatal("newTargetResolver read an object")
			}
		})
	}
}

func TestTargetCheckPolicies(t *testing.T) {
	f := newTargetWorld(t.Context())
	r := tgResolver(t, f)
	if err := r.checkPolicies(f.ctx); err != nil {
		t.Fatal("checkPolicies refused the exact guard policies")
	}
	if !slices.Equal(f.ops, []string{opGetPolicy, opGetPolicy}) || f.otherCtx || f.badQuery {
		t.Fatal("checkPolicies did not make exactly one policy read for each run namespace with the context")
	}
}

// TestTargetCheckPoliciesExact proves that the startup check uses the exact CheckPolicy result,
// and that it permits the approved extra egress rules.
func TestTargetCheckPoliciesExact(t *testing.T) {
	other := func(f *targetReader) *networkingv1.NetworkPolicy {
		return f.objs[storeKey(policyKind, tgOtherNamespace, tgPolicyName)].(*networkingv1.NetworkPolicy)
	}
	for _, tc := range []struct {
		name   string
		change func(*targetReader)
		want   error
		reads  int
	}{
		{"extra egress", func(f *targetReader) {
			other(f).Spec.Egress = append(other(f).Spec.Egress, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "192.0.2.0/24"}}}})
		}, nil, 2},
		{"no egress rules", func(f *targetReader) { other(f).Spec.Egress = nil }, nil, 2},
		{"omitted protocol", func(f *targetReader) { f.policy().Spec.Ingress[0].Ports[0].Protocol = nil }, nil, 2},
		{"missing first", func(f *targetReader) {
			f.remove(policyKind, tgNamespace, tgPolicyName)
		}, ErrTargetUnavailable, 1},
		{"missing second", func(f *targetReader) {
			f.remove(policyKind, tgOtherNamespace, tgPolicyName)
		}, ErrTargetUnavailable, 2},
		{"extra ingress port", func(f *targetReader) {
			other(f).Spec.Ingress[0].Ports = append(other(f).Spec.Ingress[0].Ports,
				networkingv1.NetworkPolicyPort{Port: new(intstr.FromInt32(22))})
		}, ErrTargetUnavailable, 2},
		{"extra ingress rule", func(f *targetReader) {
			f.policy().Spec.Ingress = append(f.policy().Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
		}, ErrTargetUnavailable, 1},
		{"udp guard port", func(f *targetReader) {
			f.policy().Spec.Ingress[0].Ports[0].Protocol = new(corev1.ProtocolUDP)
		}, ErrTargetUnavailable, 1},
		{"pod selector drift", func(f *targetReader) {
			f.policy().Spec.PodSelector = metav1.LabelSelector{}
		}, ErrTargetUnavailable, 1},
		{"no ingress type", func(f *targetReader) {
			f.policy().Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
		}, ErrTargetUnavailable, 1},
		{litCaseDeleting, func(f *targetReader) {
			f.policy().DeletionTimestamp = tgDeleting()
		}, ErrTargetUnavailable, 1},
		{litCaseAPIError, func(f *targetReader) { f.fail[1] = errors.New(targetCanary) }, ErrTargetUnavailable, 2},
		{"forbidden", func(f *targetReader) {
			f.fail[0] = apierrors.NewForbidden(networkingv1.Resource("networkpolicies"), tgPolicyName, errors.New(targetCanary))
		}, ErrTargetUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			r := tgResolver(t, f)
			tc.change(f)
			err := r.checkPolicies(f.ctx)
			if err != tc.want {
				t.Fatalf("checkPolicies returned the wrong result: got error %t, want error %t", err != nil, tc.want != nil)
			}
			if len(f.ops) != tc.reads || slices.ContainsFunc(f.ops, func(op string) bool { return op != opGetPolicy }) {
				t.Fatalf("checkPolicies made %d reads, want %d policy reads only", len(f.ops), tc.reads)
			}
		})
	}
}

func TestTargetCheckPoliciesCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f := newTargetWorld(ctx)
	if err := tgResolver(t, f).checkPolicies(ctx); err != ErrTargetUnavailable || len(f.ops) != 0 {
		t.Fatal("checkPolicies read a policy after the context ended")
	}
	during, stop := context.WithCancel(t.Context())
	defer stop()
	f = newTargetWorld(during)
	f.before = func(int) { stop() }
	if err := tgResolver(t, f).checkPolicies(during); err != ErrTargetUnavailable || len(f.ops) != 1 {
		t.Fatal("checkPolicies did not stop after the context ended during a read")
	}
	var nilResolver *targetResolver
	if nilResolver.checkPolicies(t.Context()) != ErrTargetUnavailable {
		t.Fatal("checkPolicies of a nil resolver did not refuse")
	}
}

const (
	litCaseDeleting = "deleting"
)
