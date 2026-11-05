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

package agentingress

import (
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	runNS   = "runs"
	proxyNS = "koodam-system"
	npName  = "koodam-agent"
	release = "koodam"
	team    = "team"
)

type netpol = networkingv1.NetworkPolicy

func proxyLabels() map[string]string {
	return map[string]string{nameLabel: release, instanceLabel: release, componentLabel: ProxyComponent}
}

func guard() PolicyGuard {
	return PolicyGuard{Namespace: runNS, Name: npName, ProxyNamespace: proxyNS, ProxyLabels: proxyLabels()}
}

// policy returns the expected guard, with one egress rule.
func policy() *netpol {
	tcp := corev1.ProtocolTCP
	return &netpol{
		ObjectMeta: metav1.ObjectMeta{Name: npName, Namespace: runNS},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: *sel(nil, exists(agentLabel)),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: sel(map[string]string{namespaceLabel: proxyNS}),
					PodSelector:       sel(proxyLabels()),
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromString(ReservedPortName))}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: new(intstr.FromInt32(8080))}}}},
		},
	}
}

func proxyPeer(p *netpol) *networkingv1.NetworkPolicyPeer { return &p.Spec.Ingress[0].From[0] }
func guardPort(p *netpol) *networkingv1.NetworkPolicyPort { return &p.Spec.Ingress[0].Ports[0] }

// check runs CheckPolicy and fails if the result is not want or if CheckPolicy changed its input.
func check(t *testing.T, g PolicyGuard, p *netpol, want error) {
	t.Helper()
	labels := maps.Clone(g.ProxyLabels)
	var before *netpol
	if p != nil {
		before = p.DeepCopy()
	}
	if got := CheckPolicy(g, p); got != want {
		t.Fatalf("CheckPolicy returned the wrong result: got error %t, want error %t", got != nil, want != nil)
	}
	if !maps.Equal(labels, g.ProxyLabels) || (p != nil && !equality.Semantic.DeepEqual(before, p)) {
		t.Fatal("CheckPolicy changed its input")
	}
}

func TestCheckPolicy(t *testing.T) {
	udp, sctp := corev1.ProtocolUDP, corev1.ProtocolSCTP
	ipBlock := &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}
	for _, tc := range []struct {
		name string
		edit func(*netpol)
		want error
	}{
		{"exact guard", func(*netpol) {}, nil},
		{"omitted protocol is TCP", func(p *netpol) { guardPort(p).Protocol = nil }, nil},
		{"reordered policy types", func(p *netpol) {
			p.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress, networkingv1.PolicyTypeIngress}
		}, nil},
		{"no egress rules", func(p *netpol) { p.Spec.Egress = nil }, nil},
		{"extra egress rule", func(p *netpol) {
			p.Spec.Egress = append(p.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: ipBlock}}})
		}, nil},
		{"empty match labels map", func(p *netpol) { p.Spec.PodSelector.MatchLabels = map[string]string{} }, nil},

		{"wrong name", func(p *netpol) { p.Name = "other-agent" }, ErrPolicyMissing},
		{"wrong namespace", func(p *netpol) { p.Namespace = "other" }, ErrPolicyMissing},
		{"deleting", func(p *netpol) { p.DeletionTimestamp = new(metav1.Now()) }, ErrPolicyDeleting},

		{"empty pod selector", func(p *netpol) { p.Spec.PodSelector = metav1.LabelSelector{} }, ErrPolicyPodSelector},
		{"agent label as match label", func(p *netpol) {
			p.Spec.PodSelector = *sel(map[string]string{agentLabel: "a"})
		}, ErrPolicyPodSelector},
		{"extra match label", func(p *netpol) {
			p.Spec.PodSelector.MatchLabels = map[string]string{team: "a"}
		}, ErrPolicyPodSelector},
		{"extra expression", func(p *netpol) {
			p.Spec.PodSelector.MatchExpressions = append(p.Spec.PodSelector.MatchExpressions, exists(team))
		}, ErrPolicyPodSelector},
		{"other key", func(p *netpol) { p.Spec.PodSelector = *sel(nil, exists("koodam.dev/workspace")) }, ErrPolicyPodSelector},
		{"does not exist", func(p *netpol) {
			p.Spec.PodSelector.MatchExpressions[0].Operator = metav1.LabelSelectorOpDoesNotExist
		}, ErrPolicyPodSelector},
		{"exists with values", func(p *netpol) {
			p.Spec.PodSelector.MatchExpressions[0].Values = []string{"a"}
		}, ErrPolicyPodSelector},

		{"no policy types", func(p *netpol) { p.Spec.PolicyTypes = nil }, ErrPolicyTypes},
		{"ingress only", func(p *netpol) {
			p.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
		}, ErrPolicyTypes},
		{"egress only", func(p *netpol) {
			p.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
		}, ErrPolicyTypes},
		{"extra policy type", func(p *netpol) { p.Spec.PolicyTypes = append(p.Spec.PolicyTypes, "Other") }, ErrPolicyTypes},

		{"no ingress rules", func(p *netpol) { p.Spec.Ingress = nil }, ErrPolicyIngress},
		{"empty rule", func(p *netpol) { p.Spec.Ingress[0] = networkingv1.NetworkPolicyIngressRule{} }, ErrPolicyIngress},
		{"extra empty rule", func(p *netpol) {
			p.Spec.Ingress = append(p.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
		}, ErrPolicyIngress},
		{"duplicate rule", func(p *netpol) { p.Spec.Ingress = append(p.Spec.Ingress, p.Spec.Ingress[0]) }, ErrPolicyIngress},
		{"no peer", func(p *netpol) { p.Spec.Ingress[0].From = nil }, ErrPolicyIngress},
		{"extra peer", func(p *netpol) {
			p.Spec.Ingress[0].From = append(p.Spec.Ingress[0].From, networkingv1.NetworkPolicyPeer{IPBlock: ipBlock})
		}, ErrPolicyIngress},
		{"or split peers", func(p *netpol) {
			ns, pods := proxyPeer(p).NamespaceSelector, proxyPeer(p).PodSelector
			p.Spec.Ingress[0].From = []networkingv1.NetworkPolicyPeer{{NamespaceSelector: ns}, {PodSelector: pods}}
		}, ErrPolicyIngress},
		{"namespace only peer", func(p *netpol) { proxyPeer(p).PodSelector = nil }, ErrPolicyIngress},
		{"pod only peer", func(p *netpol) { proxyPeer(p).NamespaceSelector = nil }, ErrPolicyIngress},
		{"peer with ip block", func(p *netpol) { proxyPeer(p).IPBlock = ipBlock }, ErrPolicyIngress},
		{"wrong proxy namespace", func(p *netpol) {
			proxyPeer(p).NamespaceSelector.MatchLabels[namespaceLabel] = "other"
		}, ErrPolicyIngress},
		{"all namespaces", func(p *netpol) { proxyPeer(p).NamespaceSelector = sel(nil) }, ErrPolicyIngress},
		{"extra namespace label", func(p *netpol) { proxyPeer(p).NamespaceSelector.MatchLabels[team] = "a" }, ErrPolicyIngress},
		{"extra namespace expression", func(p *netpol) {
			proxyPeer(p).NamespaceSelector.MatchExpressions = []req{exists(team)}
		}, ErrPolicyIngress},
		{"all pods", func(p *netpol) { proxyPeer(p).PodSelector = sel(nil) }, ErrPolicyIngress},
		{"missing proxy label", func(p *netpol) { delete(proxyPeer(p).PodSelector.MatchLabels, instanceLabel) }, ErrPolicyIngress},
		{"extra proxy label", func(p *netpol) { proxyPeer(p).PodSelector.MatchLabels[team] = "a" }, ErrPolicyIngress},
		{"egress proxy pods", func(p *netpol) {
			proxyPeer(p).PodSelector.MatchLabels[componentLabel] = "egress-proxy"
		}, ErrPolicyIngress},
		{"extra proxy expression", func(p *netpol) { proxyPeer(p).PodSelector.MatchExpressions = []req{exists(team)} }, ErrPolicyIngress},

		{"no ports", func(p *netpol) { p.Spec.Ingress[0].Ports = nil }, ErrPolicyIngress},
		{"extra port", func(p *netpol) {
			p.Spec.Ingress[0].Ports = append(p.Spec.Ingress[0].Ports, p.Spec.Ingress[0].Ports[0])
		}, ErrPolicyIngress},
		{"nil port", func(p *netpol) { guardPort(p).Port = nil }, ErrPolicyIngress},
		{"numeric port", func(p *netpol) { guardPort(p).Port = new(intstr.FromInt32(8080)) }, ErrPolicyIngress},
		{"numeric type with http text", func(p *netpol) {
			guardPort(p).Port = &intstr.IntOrString{Type: intstr.Int, IntVal: 8080, StrVal: ReservedPortName}
		}, ErrPolicyIngress},
		{"other port name", func(p *netpol) { guardPort(p).Port = new(intstr.FromString("https")) }, ErrPolicyIngress},
		{"other case port name", func(p *netpol) { guardPort(p).Port = new(intstr.FromString("HTTP")) }, ErrPolicyIngress},
		{"udp", func(p *netpol) { guardPort(p).Protocol = &udp }, ErrPolicyIngress},
		{"sctp", func(p *netpol) { guardPort(p).Protocol = &sctp }, ErrPolicyIngress},
		{"end port", func(p *netpol) { guardPort(p).EndPort = new(int32(9000)) }, ErrPolicyIngress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy()
			tc.edit(p)
			check(t, guard(), p, tc.want)
		})
	}
}

func TestCheckPolicyMissing(t *testing.T) {
	check(t, guard(), nil, ErrPolicyMissing)
}

func TestCheckPolicyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PolicyGuard)
	}{
		{"empty namespace", func(g *PolicyGuard) { g.Namespace = "" }},
		{"invalid namespace", func(g *PolicyGuard) { g.Namespace = "Runs" }},
		{"empty name", func(g *PolicyGuard) { g.Name = "" }},
		{"invalid name", func(g *PolicyGuard) { g.Name = "bad_name" }},
		{"empty proxy namespace", func(g *PolicyGuard) { g.ProxyNamespace = "" }},
		{"invalid proxy namespace", func(g *PolicyGuard) { g.ProxyNamespace = "koodam.system" }},
		{"nil proxy labels", func(g *PolicyGuard) { g.ProxyLabels = nil }},
		{"empty proxy labels", func(g *PolicyGuard) { g.ProxyLabels = map[string]string{} }},
		{"component only", func(g *PolicyGuard) {
			g.ProxyLabels = map[string]string{componentLabel: ProxyComponent}
		}},
		{"egress proxy component", func(g *PolicyGuard) { g.ProxyLabels[componentLabel] = "egress-proxy" }},
		{"other case component", func(g *PolicyGuard) { g.ProxyLabels[componentLabel] = "Ingress-Proxy" }},
		{"missing name label", func(g *PolicyGuard) { delete(g.ProxyLabels, nameLabel) }},
		{"missing instance label", func(g *PolicyGuard) { delete(g.ProxyLabels, instanceLabel) }},
		{"empty name label", func(g *PolicyGuard) { g.ProxyLabels[nameLabel] = "" }},
		{"invalid instance label", func(g *PolicyGuard) { g.ProxyLabels[instanceLabel] = "bad value" }},
		{"extra proxy label", func(g *PolicyGuard) { g.ProxyLabels[team] = "a" }},
		{"other key in place of name", func(g *PolicyGuard) {
			delete(g.ProxyLabels, nameLabel)
			g.ProxyLabels[team] = release
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := guard()
			tc.edit(&g)
			if g.Validate() != ErrPolicyConfig {
				t.Fatal("Validate permitted a configuration that is not valid")
			}
			check(t, g, policy(), ErrPolicyConfig)
		})
	}
	if guard().Validate() != nil {
		t.Fatal("Validate refused the chart configuration")
	}
}
