/*
Copyright 2026.

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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const egressSpace = "egress-team"

var testEgress = EgressProxy{IP: "10.96.0.53", Port: 3128, ClusterDomain: "cluster.local"}

var _ = Describe("Egress proxy", func() {
	It("points the gateway URL to the /llm route of the proxy", func() {
		Expect(testEgress.GatewayURL("http://gw.test:4000")).To(Equal("http://10.96.0.53:3128/llm"))
		Expect(EgressProxy{}.GatewayURL("http://gw.test:4000")).To(Equal("http://gw.test:4000"))
	})

	It("keeps the DNS of the pod with no proxy, and sets the default dnsPolicy", func() {
		pod := corev1.PodSpec{DNSPolicy: corev1.DNSDefault}
		EgressProxy{}.apply(&pod, egressSpace)
		Expect(pod.DNSPolicy).To(Equal(corev1.DNSDefault))
		Expect(pod.DNSConfig).To(BeNil())
		pod = corev1.PodSpec{}
		EgressProxy{}.apply(&pod, egressSpace)
		Expect(pod.DNSPolicy).To(Equal(corev1.DNSClusterFirst))
	})

	It("replaces the DNS of a Workspace spec.podTemplate", func() {
		ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: egressSpace},
			Spec: corev1alpha1.WorkspaceSpec{PodTemplate: &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
				DNSPolicy:   corev1.DNSDefault,
				DNSConfig:   &corev1.PodDNSConfig{Nameservers: []string{"8.8.8.8"}},
				HostAliases: []corev1.HostAlias{{IP: "203.0.113.1", Hostnames: []string{"api.example.com"}}}}}}}
		r := &WorkspaceReconciler{Image: "example.com/ws:1", APIReader: k8sClient, Egress: testEgress}
		sts, err := r.mergedStatefulSet(ctx, ws, testProfile)
		Expect(err).NotTo(HaveOccurred())
		pod := sts.Spec.Template.Spec
		Expect(pod.DNSPolicy).To(Equal(corev1.DNSNone))
		Expect(pod.DNSConfig).To(Equal(&corev1.PodDNSConfig{
			Nameservers: []string{"10.96.0.53"},
			Searches:    []string{egressSpace + ".svc.cluster.local", "svc.cluster.local", "cluster.local"},
			Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: new("5")}},
		}))
		Expect(pod.HostAliases).To(BeEmpty())
	})

	It("sets the TracedEgress condition", func() {
		c := testEgress.condition(3)
		Expect(c.Type).To(Equal(condTracedEgress))
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.ObservedGeneration).To(Equal(int64(3)))
		c = EgressProxy{}.condition(3)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal("NoProxy"))
	})
})
