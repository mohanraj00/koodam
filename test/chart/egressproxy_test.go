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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const proxyName = "koodam-egress-proxy"

// proxyValues turn on the proxy with a fixed ClusterIP.
const proxyValues = `
egressProxy:
  enabled: true
  clusterIP: 10.96.0.50
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

	svcs := decode[corev1.Service](t, out, "Service", proxyName)
	if len(svcs) != 1 || svcs[0].Spec.ClusterIP != "10.96.0.50" ||
		len(svcs[0].Spec.Ports) != 1 || svcs[0].Spec.Ports[0].Port != 3128 {
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
	c := pod.Containers[0]
	if !slices.Equal(c.Command, []string{"/egress-proxy"}) {
		t.Fatalf("got the command %q", c.Command)
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
	if len(crbs) != 1 || crbs[0].Subjects[0].Name != proxyName || crbs[0].Subjects[0].Namespace != "koodam-system" {
		t.Fatalf("got the ClusterRoleBindings %+v", crbs)
	}
	if roles := decode[rbacv1.Role](t, out, "Role", proxyName); len(roles) != 0 {
		t.Fatalf("got %d proxy Roles with no namespace, want 0", len(roles))
	}

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
	if rbs := decode[rbacv1.RoleBinding](t, out, "RoleBinding", proxyName); len(rbs) != 2 {
		t.Fatalf("got %d proxy RoleBindings, want 2", len(rbs))
	}
	if crs := decode[rbacv1.ClusterRole](t, out, "ClusterRole", proxyName); len(crs) != 0 {
		t.Fatal("the proxy has a ClusterRole, but the namespaces are known")
	}
	deps := decode[appsv1.Deployment](t, out, "Deployment", proxyName)
	if !slices.Contains(deps[0].Spec.Template.Spec.Containers[0].Args, "--namespaces=dev-a,dev-b") {
		t.Fatal("the proxy does not watch only the known namespaces")
	}
}

func TestEgressProxyRefusesNoClusterIP(t *testing.T) {
	if out, err := helmTemplate(t, "", "egressProxy.enabled=true"); err == nil || !strings.Contains(out, "clusterIP") {
		t.Fatalf("got no error about the clusterIP:\n%s", out)
	}
}
