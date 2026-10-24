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
	"net"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const condTracedEgress = "TracedEgress"

// EgressProxy sends the egress of the Agent and Workspace pods through the egress proxy (#142).
// The proxy is the DNS server of the pods. It answers each name outside the cluster domain with
// its own IP, thus each call to the internet goes to the proxy. If IP is empty, the pods use the
// cluster DNS and the gateway Service.
type EgressProxy struct {
	// IP is the ClusterIP of the proxy Service.
	IP string
	// Port is the HTTP port of the proxy, which has the /llm route to the gateway.
	Port int
	// ClusterDomain is the DNS domain of the cluster, for example cluster.local.
	ClusterDomain string
}

// GatewayURL returns the gateway address for the pods: the /llm route of the proxy, or gatewayURL
// if there is no proxy.
func (e EgressProxy) GatewayURL(gatewayURL string) string {
	if e.IP == "" {
		return gatewayURL
	}
	return "http://" + net.JoinHostPort(e.IP, strconv.Itoa(e.Port)) + "/llm"
}

// apply sets the DNS of the pod to the proxy. It replaces a dnsPolicy, a dnsConfig and the
// hostAliases from spec.podTemplate, because they let the pod go around the proxy DNS. The
// NetworkPolicy of #143 blocks a call to an IP address that does not go to the proxy.
// With no proxy, an empty dnsPolicy becomes ClusterFirst, the API default. Thus the StatefulSet
// compare finds a change after the proxy goes off, and the pod gets the cluster DNS again.
func (e EgressProxy) apply(pod *corev1.PodSpec, namespace string) {
	if e.IP == "" {
		if pod.DNSPolicy == "" {
			pod.DNSPolicy = corev1.DNSClusterFirst
		}
		return
	}
	pod.HostAliases = nil
	d := e.ClusterDomain
	pod.DNSPolicy = corev1.DNSNone
	pod.DNSConfig = &corev1.PodDNSConfig{
		Nameservers: []string{e.IP},
		Searches:    []string{namespace + ".svc." + d, "svc." + d, d},
		Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: new("5")}},
	}
}

// gatewayURLAnnotation is the gateway URL on the Workspace pod template. The kubelet does not
// update the files of an immutable Secret. Thus a new URL changes the template, and the pod
// starts again with the new Secret.
const gatewayURLAnnotation = "koodam.dev/gateway-url"

// annotate puts the gateway URL on the pod template if the proxy is on and the pod has a gateway
// key. With no proxy, the template does not change, thus an upgrade does not restart the pods.
func (e EgressProxy) annotate(m *metav1.ObjectMeta, gateway bool, url string) {
	if e.IP == "" || !gateway {
		return
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	m.Annotations[gatewayURLAnnotation] = url
}

// condition is True if the pods use the proxy, else False with the reason NoProxy.
func (e EgressProxy) condition(generation int64) metav1.Condition {
	c := metav1.Condition{Type: condTracedEgress, Status: metav1.ConditionTrue, Reason: "Proxy",
		Message: "The pod DNS and the gateway calls go through the egress proxy.", ObservedGeneration: generation}
	if e.IP == "" {
		c.Status, c.Reason = metav1.ConditionFalse, "NoProxy"
		c.Message = "The controller has no --egress-proxy-ip, so the pod egress has no trace."
	}
	return c
}
