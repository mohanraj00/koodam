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
	"errors"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The label keys of the expected guard. The chart sets the same keys.
const (
	agentLabel     = "koodam.dev/agent"
	namespaceLabel = "kubernetes.io/metadata.name"
	nameLabel      = "app.kubernetes.io/name"
	instanceLabel  = "app.kubernetes.io/instance"
	componentLabel = "app.kubernetes.io/component"

	// ProxyComponent is the component label value of the ingress proxy pods.
	ProxyComponent = "ingress-proxy"
)

// The errors have fixed text. They never contain a value from the configuration or the policy.
var (
	ErrPolicyConfig      = errors.New("the ingress NetworkPolicy guard configuration is not valid")
	ErrPolicyMissing     = errors.New("the Agent NetworkPolicy is missing or is not the configured object")
	ErrPolicyDeleting    = errors.New("the Agent NetworkPolicy is being deleted")
	ErrPolicyPodSelector = errors.New("the Agent NetworkPolicy does not select exactly the Agent pods")
	ErrPolicyTypes       = errors.New("the Agent NetworkPolicy policy types are not Ingress and Egress")
	ErrPolicyIngress     = errors.New("the Agent NetworkPolicy ingress rules are not the expected guard")
)

// PolicyGuard is the expected ingress guard of the Agent NetworkPolicy in one run namespace.
type PolicyGuard struct {
	// Namespace is the run namespace of the Agent NetworkPolicy.
	Namespace string
	// Name is the chart name of the Agent NetworkPolicy, <fullname trunc 51>-agent.
	Name string
	// ProxyNamespace is the namespace of the ingress proxy pods.
	ProxyNamespace string
	// ProxyLabels is the chart pod selector of the ingress proxy pods.
	ProxyLabels map[string]string
}

// Validate returns ErrPolicyConfig if a name is not a valid Kubernetes name, or if ProxyLabels is not
// exactly the chart ingress proxy selector.
func (g PolicyGuard) Validate() error {
	if len(validation.IsDNS1123Label(g.Namespace)) > 0 ||
		len(validation.IsDNS1123Subdomain(g.Name)) > 0 ||
		len(validation.IsDNS1123Label(g.ProxyNamespace)) > 0 {
		return ErrPolicyConfig
	}
	if len(g.ProxyLabels) != 3 || g.ProxyLabels[componentLabel] != ProxyComponent {
		return ErrPolicyConfig
	}
	for _, k := range []string{nameLabel, instanceLabel} {
		if v, ok := g.ProxyLabels[k]; !ok || v == "" || len(validation.IsValidLabelValue(v)) > 0 {
			return ErrPolicyConfig
		}
	}
	return nil
}

// CheckPolicy returns nil only if obj is the configured Agent NetworkPolicy and its pod selector,
// policy types and ingress rules are exactly the guard. It does not compare the egress rules. It
// does not change g or obj. A nil error proves the shape of the object only; it does not prove that
// the CNI enforces it, or that no other additive policy permits more traffic.
func CheckPolicy(g PolicyGuard, obj *networkingv1.NetworkPolicy) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if obj == nil || obj.Name != g.Name || obj.Namespace != g.Namespace {
		return ErrPolicyMissing
	}
	if obj.DeletionTimestamp != nil {
		return ErrPolicyDeleting
	}
	agents := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: agentLabel, Operator: metav1.LabelSelectorOpExists}}}
	if !sameSelector(&obj.Spec.PodSelector, agents) {
		return ErrPolicyPodSelector
	}
	if !ingressAndEgress(obj.Spec.PolicyTypes) {
		return ErrPolicyTypes
	}
	if !guardRules(g, obj.Spec.Ingress) {
		return ErrPolicyIngress
	}
	return nil
}

// ingressAndEgress is true if the set of ts is exactly {Ingress, Egress}.
func ingressAndEgress(ts []networkingv1.PolicyType) bool {
	var in, eg bool
	for _, t := range ts {
		switch t {
		case networkingv1.PolicyTypeIngress:
			in = true
		case networkingv1.PolicyTypeEgress:
			eg = true
		default:
			return false
		}
	}
	return in && eg
}

// guardRules is true if rules is one rule with one peer, the proxy namespace and pod selectors
// together, and one TCP port with the name http.
func guardRules(g PolicyGuard, rules []networkingv1.NetworkPolicyIngressRule) bool {
	if len(rules) != 1 || len(rules[0].From) != 1 || len(rules[0].Ports) != 1 {
		return false
	}
	peer := rules[0].From[0]
	ns := &metav1.LabelSelector{MatchLabels: map[string]string{namespaceLabel: g.ProxyNamespace}}
	pods := &metav1.LabelSelector{MatchLabels: g.ProxyLabels}
	if peer.IPBlock != nil || !sameSelector(peer.NamespaceSelector, ns) || !sameSelector(peer.PodSelector, pods) {
		return false
	}
	p := rules[0].Ports[0]
	if p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP {
		return false
	}
	return p.EndPort == nil && p.Port != nil && p.Port.Type == intstr.String && p.Port.StrVal == ReservedPortName
}
