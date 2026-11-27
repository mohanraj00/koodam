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
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestWorkspaceNetworkPolicySSHFrom(t *testing.T) {
	out, err := helmTemplate(t, `
workspace:
  networkPolicy:
    namespaces: [dev-a]
    sshFrom:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: access
      podSelector:
        matchLabels:
          app: bastion
`)
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	nps := policies(t, out)
	if len(nps) != 1 {
		t.Fatalf("got %d NetworkPolicies, want 1", len(nps))
	}
	spec := nps[0].Spec
	if !slices.Contains(spec.PolicyTypes, networkingv1.PolicyTypeIngress) {
		t.Errorf("the policy types are %v, want Ingress", spec.PolicyTypes)
	}
	want := []networkingv1.NetworkPolicyIngressRule{{
		From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "access"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "bastion"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{
			Protocol: new(corev1.ProtocolTCP),
			Port:     new(intstr.FromInt32(2222)),
		}},
	}}
	if !reflect.DeepEqual(spec.Ingress, want) {
		t.Errorf("the ingress is %v, want TCP 2222 from the bastion peer only", spec.Ingress)
	}
}
