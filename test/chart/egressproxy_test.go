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
	"maps"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const (
	proxyName = "koodam-egress-proxy"
	releaseNS = "koodam-system"
)

// proxyValues turn on the proxy with a fixed ClusterIP and an API server rule.
const proxyValues = `
egressProxy:
  enabled: true
  clusterIP: 10.96.0.50
  networkPolicy:
    apiServer:
    - to:
      - ipBlock:
          cidr: 172.18.0.2/32
      ports:
      - protocol: TCP
        port: 6443
`

// decode returns the rendered objects of one kind with the given name.
func decode[T any](t *testing.T, out, kind, name string) []T {
	t.Helper()
	var got []T
	for _, doc := range objects(t, out, kind) {
		var meta struct{ Metadata struct{ Name string } }
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Metadata.Name != name {
			continue
		}
		var o T
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatal(err)
		}
		got = append(got, o)
	}
	return got
}

func render(t *testing.T, values string, set ...string) string {
	t.Helper()
	out, err := helmTemplate(t, values, set...)
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	return out
}

// checkPodRules makes sure that the rules give only get, list and watch on pods.
func checkPodRules(t *testing.T, rules []rbacv1.PolicyRule) {
	t.Helper()
	if len(rules) != 1 || !slices.Equal(rules[0].APIGroups, []string{""}) ||
		!slices.Equal(rules[0].Resources, []string{"pods"}) ||
		!slices.Equal(rules[0].Verbs, []string{"get", "list", "watch"}) {
		t.Fatalf("got the rules %+v, want get, list and watch on pods", rules)
	}
}

func TestEgressProxyDefaultIsOff(t *testing.T) {
	out := render(t, "")
	if strings.Contains(out, "egress-proxy") {
		t.Fatal("the default values render an egress proxy object")
	}
}

func TestEgressProxy(t *testing.T) {
	out := render(t, proxyValues)

	if sas := decode[corev1.ServiceAccount](t, out, "ServiceAccount", proxyName); len(sas) != 1 ||
		sas[0].Namespace != releaseNS {
		t.Fatalf("got the ServiceAccounts %+v, want 1 in koodam-system", sas)
	}
	svcs := decode[corev1.Service](t, out, "Service", proxyName)
	if len(svcs) != 1 || svcs[0].Spec.ClusterIP != "10.96.0.50" || len(svcs[0].Spec.Ports) != 1 ||
		svcs[0].Spec.Ports[0].Port != 3128 || svcs[0].Spec.Ports[0].TargetPort.String() != "proxy" {
		t.Fatalf("got the Services %+v, want 1 on 10.96.0.50:3128", svcs)
	}

	deps := decode[appsv1.Deployment](t, out, "Deployment", proxyName)
	if len(deps) != 1 {
		t.Fatalf("got %d proxy Deployments, want 1", len(deps))
	}
	pod := deps[0].Spec.Template.Spec
	if pod.ServiceAccountName != proxyName || pod.SecurityContext == nil ||
		pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("the pod spec is wrong: %+v", pod)
	}
	if sel := deps[0].Spec.Selector.MatchLabels; !maps.Equal(sel, svcs[0].Spec.Selector) ||
		!maps.Equal(sel, deps[0].Spec.Template.Labels) {
		t.Fatalf("the Service selector %v, the Deployment selector %v and the pod labels %v do not agree",
			svcs[0].Spec.Selector, sel, deps[0].Spec.Template.Labels)
	}
	c := pod.Containers[0]
	if !slices.Equal(c.Command, []string{"/egress-proxy"}) {
		t.Fatalf("got the command %q", c.Command)
	}
	if c.LivenessProbe.HTTPGet.Path != "/healthz" || c.ReadinessProbe.HTTPGet.Path != "/readyz" ||
		!slices.Contains(c.Args, "--probe-address=:8081") {
		t.Fatalf("the probes do not agree with the probe address: %q", c.Args)
	}
	for _, want := range []string{"--listen=:3128", "--gateway-url=http://koodam-litellm.koodam-system.svc:4000",
		"--http-ports=80", "--connect-ports=443"} {
		if !slices.Contains(c.Args, want) {
			t.Errorf("the args %q have no %q", c.Args, want)
		}
	}
	sc := c.SecurityContext
	if sc == nil || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation ||
		!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatalf("the container security context is not restricted: %+v", sc)
	}

	crs := decode[rbacv1.ClusterRole](t, out, "ClusterRole", proxyName)
	if len(crs) != 1 {
		t.Fatalf("got %d proxy ClusterRoles, want 1", len(crs))
	}
	checkPodRules(t, crs[0].Rules)
	crbs := decode[rbacv1.ClusterRoleBinding](t, out, "ClusterRoleBinding", proxyName)
	if len(crbs) != 1 || crbs[0].Subjects[0].Name != proxyName || crbs[0].Subjects[0].Namespace != releaseNS {
		t.Fatalf("got the ClusterRoleBindings %+v", crbs)
	}
	if roles := decode[rbacv1.Role](t, out, "Role", proxyName); len(roles) != 0 {
		t.Fatalf("got %d proxy Roles with no namespace, want 0", len(roles))
	}
}

func TestEgressProxyNetworkPolicy(t *testing.T) {
	nps := policies(t, render(t, proxyValues))
	if len(nps) != 1 || nps[0].Name != proxyName {
		t.Fatalf("got %d NetworkPolicies, want the proxy policy only", len(nps))
	}
	ing := nps[0].Spec.Ingress
	if len(ing) != 2 || len(ing[0].Ports) != 1 || ing[0].Ports[0].Port.IntValue() != 3128 ||
		len(ing[1].From) != 0 || len(ing[1].Ports) != 1 || ing[1].Ports[0].Port.IntValue() != 8081 {
		t.Fatalf("got the ingress rules %+v, want the proxy port and the probe port", ing)
	}
	keys := make([]string, 0, len(ing[0].From))
	for _, from := range ing[0].From {
		if from.NamespaceSelector == nil || from.PodSelector == nil || len(from.PodSelector.MatchExpressions) != 1 {
			t.Fatalf("the proxy port peer %+v does not select pods by a label", from)
		}
		keys = append(keys, from.PodSelector.MatchExpressions[0].Key)
	}
	if !slices.Equal(keys, []string{"koodam.dev/agent", "koodam.dev/workspace"}) {
		t.Fatalf("the proxy port is open to %q, want the Agent and Workspace pods only", keys)
	}
	egress := nps[0].Spec.Egress
	// DNS, the bundled gateway, the API server and the public internet.
	if len(egress) != 4 || egress[2].To[0].IPBlock.CIDR != "172.18.0.2/32" {
		t.Fatalf("got the egress rules %+v", egress)
	}
	checkPublicRule(t, egress[3], 80, 443)
}

func TestEgressProxyNamespaces(t *testing.T) {
	out := render(t, proxyValues, "workspace.networkPolicy.namespaces={dev-a,dev-b}")
	roles := decode[rbacv1.Role](t, out, "Role", proxyName)
	if len(roles) != 2 || roles[0].Namespace != "dev-a" || roles[1].Namespace != "dev-b" {
		t.Fatalf("got %d proxy Roles, want 1 in dev-a and 1 in dev-b", len(roles))
	}
	for _, r := range roles {
		checkPodRules(t, r.Rules)
	}
	rbs := decode[rbacv1.RoleBinding](t, out, "RoleBinding", proxyName)
	if len(rbs) != 2 {
		t.Fatalf("got %d proxy RoleBindings, want 2", len(rbs))
	}
	for i, rb := range rbs {
		if rb.Namespace != roles[i].Namespace || rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != proxyName ||
			len(rb.Subjects) != 1 || rb.Subjects[0].Kind != "ServiceAccount" ||
			rb.Subjects[0].Name != proxyName || rb.Subjects[0].Namespace != releaseNS {
			t.Fatalf("the RoleBinding %+v does not bind the proxy ServiceAccount to the Role", rb)
		}
	}
	if crs := decode[rbacv1.ClusterRole](t, out, "ClusterRole", proxyName); len(crs) != 0 {
		t.Fatal("the proxy has a ClusterRole, but the namespaces are known")
	}
	if crbs := decode[rbacv1.ClusterRoleBinding](t, out, "ClusterRoleBinding", proxyName); len(crbs) != 0 {
		t.Fatal("the proxy has a ClusterRoleBinding, but the namespaces are known")
	}
	deps := decode[appsv1.Deployment](t, out, "Deployment", proxyName)
	if !slices.Contains(deps[0].Spec.Template.Spec.Containers[0].Args, "--namespaces=dev-a,dev-b") {
		t.Fatal("the proxy does not watch only the known namespaces")
	}
}

func TestEgressProxyRefusesBadValues(t *testing.T) {
	for want, values := range map[string]string{
		"clusterIP":    "egressProxy: {enabled: true, networkPolicy: {enabled: false}}",
		"apiServer":    "egressProxy: {enabled: true, clusterIP: 10.96.0.50}",
		"httpPorts":    "egressProxy: {enabled: true, clusterIP: 10.96.0.50, httpPorts: null}",
		"connectPorts": "egressProxy: {enabled: true, clusterIP: 10.96.0.50, connectPorts: []}",
	} {
		if out, err := helmTemplate(t, values); err == nil || !strings.Contains(out, want) {
			t.Errorf("got no error about %s:\n%s", want, out)
		}
	}
	out := render(t, "egressProxy: {enabled: true, clusterIP: 10.96.0.50, networkPolicy: {enabled: false}}")
	if nps := policies(t, out); len(nps) != 0 {
		t.Fatal("the proxy NetworkPolicy is on, but networkPolicy.enabled is false")
	}
}
