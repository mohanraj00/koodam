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

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// gateDNSNamespace is the namespace of the cluster DNS.
	gateDNSNamespace = "kube-system"
	// gateDNSPort is the port of the cluster DNS.
	gateDNSPort = 53
)

// gatePassed is true if the weights of the spec apply to the version: the version has no gate, or
// the controller accepted a record with the verdict pass.
func gatePassed(av *corev1alpha1.AppVersion) bool {
	return av.Spec.Gate == nil || (av.Status.Evidence != nil && av.Status.Evidence.Verdict == corev1alpha1.EvidencePass &&
		meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionEvidence))
}

// gateOpen is true while the gate of the version has no end state. Only then the gate pod needs a
// path to the pods of the version.
func gateOpen(av *corev1alpha1.AppVersion) bool {
	return av.Spec.Gate != nil && av.Status.Evidence == nil &&
		!meta.IsStatusConditionFalse(av.Status.Conditions, corev1alpha1.AppVersionEvidence)
}

func gatePorts(ports []corev1.ContainerPort) []networkingv1.NetworkPolicyPort {
	tcp := corev1.ProtocolTCP
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: new(intstr.FromInt32(p.ContainerPort))})
	}
	return out
}

// gateIngress is the rule of the NetworkPolicy of the version that permits ingress from the gate
// pod of this version, on the ports of the version.
func gateIngress(av *corev1alpha1.AppVersion, ports []corev1.ContainerPort) networkingv1.NetworkPolicyIngressRule {
	return networkingv1.NetworkPolicyIngressRule{Ports: gatePorts(ports), From: []networkingv1.NetworkPolicyPeer{{
		PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{gateLabel: av.Name}}}}}
}

// gatePolicy is the NetworkPolicy of the gate pod. The pod gets no ingress. It gets egress only to
// the pods of the version on their ports, and to the cluster DNS.
func gatePolicy(av *corev1alpha1.AppVersion, ports []corev1.ContainerPort) *networkingv1.NetworkPolicy {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	dns := new(intstr.FromInt32(gateDNSPort))
	m := appVersionMeta(av)
	m.Name, m.Labels = gateName(av), map[string]string{gateLabel: av.Name}
	return &networkingv1.NetworkPolicy{ObjectMeta: m, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{gateLabel: av.Name}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{Ports: gatePorts(ports), To: []networkingv1.NetworkPolicyPeer{{
				PodSelector: &metav1.LabelSelector{MatchLabels: appVersionLabels(av)}}}},
			{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: dns}, {Protocol: &tcp, Port: dns}},
				To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{corev1.LabelMetadataName: gateDNSNamespace}}}}},
		},
	}}
}

// applyGatePolicy makes the NetworkPolicy of the gate pod, or makes it equal to want. It gives
// back errNotOwned if a policy with the name is not from av.
func (r *AppVersionReconciler) applyGatePolicy(ctx context.Context, av *corev1alpha1.AppVersion,
	want *networkingv1.NetworkPolicy) error {
	err := r.Create(ctx, want.DeepCopy())
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	have := &networkingv1.NetworkPolicy{}
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(want), have); err != nil {
		return err
	}
	if !metav1.IsControlledBy(have, av) {
		return errNotOwned
	}
	if equality.Semantic.DeepEqual(have.Spec, want.Spec) {
		return nil
	}
	have.Spec = want.Spec
	return r.Update(ctx, have)
}
