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

package chart

import (
	"slices"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

// policies returns the rendered Workspace NetworkPolicies.
func policies(t *testing.T, out string) []networkingv1.NetworkPolicy {
	t.Helper()
	docs := objects(t, out, "NetworkPolicy")
	nps := make([]networkingv1.NetworkPolicy, 0, len(docs))
	for _, doc := range docs {
		var np networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(doc), &np); err != nil {
			t.Fatal(err)
		}
		nps = append(nps, np)
	}
	return nps
}

func TestWorkspaceNetworkPolicyDefaultIsNone(t *testing.T) {
	out, err := helmTemplate(t, "")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	if nps := policies(t, out); len(nps) != 0 {
		t.Fatalf("got %d NetworkPolicies with no namespace, want 0", len(nps))
	}
}

func TestWorkspaceNetworkPolicy(t *testing.T) {
	out, err := helmTemplate(t, "", "workspace.networkPolicy.namespaces={dev-a,dev-b}")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	nps := policies(t, out)
	if len(nps) != 2 || nps[0].Namespace != "dev-a" || nps[1].Namespace != "dev-b" {
		t.Fatalf("got %d NetworkPolicies, want 1 in dev-a and 1 in dev-b", len(nps))
	}
	np := nps[0]
	if sel := np.Spec.PodSelector.MatchExpressions; len(sel) != 1 || sel[0].Key != "koodam.dev/workspace" {
		t.Errorf("the pod selector is %v, want the Workspace label", np.Spec.PodSelector)
	}
	if !slices.Equal(np.Spec.PolicyTypes, []networkingv1.PolicyType{"Ingress", "Egress"}) {
		t.Errorf("the policy types are %v, want Ingress and Egress", np.Spec.PolicyTypes)
	}
	in := np.Spec.Ingress
	if len(in) != 1 || len(in[0].Ports) != 1 || in[0].Ports[0].Port.IntValue() != 2222 {
		t.Errorf("the ingress is %v, want only TCP 2222", in)
	}
	eg := np.Spec.Egress
	if len(eg) != 3 {
		t.Fatalf("got %d egress rules, want DNS, the gateway and the public internet", len(eg))
	}
	dns := eg[0]
	if dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" ||
		dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" || len(dns.Ports) != 2 ||
		dns.Ports[0].Port.IntValue() != 53 || dns.Ports[1].Port.IntValue() != 53 {
		t.Errorf("the first egress rule is %v, want UDP and TCP 53 to kube-dns", dns)
	}
	if eg[1].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "koodam-system" ||
		eg[1].To[0].PodSelector.MatchLabels["app.kubernetes.io/component"] != "litellm" ||
		eg[1].Ports[0].Port.IntValue() != 4000 {
		t.Errorf("the second egress rule is %v, want the bundled LiteLLM on 4000", eg[1])
	}
	v4 := eg[2].To[0].IPBlock
	for _, private := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"} {
		if v4 == nil || !slices.Contains(v4.Except, private) {
			t.Errorf("the public internet rule does not block %s", private)
		}
	}
	v6 := eg[2].To[1].IPBlock
	for _, private := range []string{"::ffff:0:0/96", "64:ff9b::/96", "fc00::/7", "fe80::/10"} {
		if v6 == nil || v6.CIDR != "::/0" || !slices.Contains(v6.Except, private) {
			t.Errorf("the public internet rule does not block %s", private)
		}
	}
	ports := make([]int, 0, len(eg[2].Ports))
	for _, p := range eg[2].Ports {
		ports = append(ports, p.Port.IntValue())
	}
	if !slices.Equal(ports, []int{443, 80, 22}) {
		t.Errorf("the public internet ports are %v, want 443, 80 and 22", ports)
	}
}

func TestWorkspaceNetworkPolicyBlockedCIDRs(t *testing.T) {
	out, err := helmTemplate(t, "", "workspace.networkPolicy.namespaces={dev-a}",
		"workspace.networkPolicy.blockedCIDRs={203.0.113.0/24,2001:db8::/32}",
		"workspace.networkPolicy.publicInternetPorts=null")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	public := policies(t, out)[0].Spec.Egress[2]
	if !slices.Contains(public.To[0].IPBlock.Except, "203.0.113.0/24") ||
		slices.Contains(public.To[0].IPBlock.Except, "2001:db8::/32") {
		t.Errorf("the IPv4 except list is %v, want the IPv4 range only", public.To[0].IPBlock.Except)
	}
	if !slices.Contains(public.To[1].IPBlock.Except, "2001:db8::/32") {
		t.Errorf("the IPv6 except list is %v, want the IPv6 range", public.To[1].IPBlock.Except)
	}
	if len(public.Ports) != 0 {
		t.Errorf("the public internet ports are %v, want all ports", public.Ports)
	}
}

func TestWorkspaceNetworkPolicyNullValue(t *testing.T) {
	out, err := helmTemplate(t, "workspace:\n  networkPolicy: null\n")
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	if nps := policies(t, out); len(nps) != 0 {
		t.Fatalf("got %d NetworkPolicies, want 0", len(nps))
	}
}

func TestWorkspaceNetworkPolicyNoInternetExistingGateway(t *testing.T) {
	out, err := helmTemplate(t, `
gateway:
  mode: existing
  url: http://litellm.example:4000
workspace:
  networkPolicy:
    namespaces: [dev-a]
    publicInternet: false
    extraEgress:
    - to:
      - ipBlock:
          cidr: 10.1.2.3/32
      ports:
      - port: 4000
`)
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	nps := policies(t, out)
	if len(nps) != 1 {
		t.Fatalf("got %d NetworkPolicies, want 1", len(nps))
	}
	eg := nps[0].Spec.Egress
	if len(eg) != 2 || eg[1].To[0].IPBlock == nil || eg[1].To[0].IPBlock.CIDR != "10.1.2.3/32" {
		t.Fatalf("the egress is %v, want DNS and the extra rule only", eg)
	}
}
