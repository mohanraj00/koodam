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
	rbacv1 "k8s.io/api/rbac/v1"
)

// The values that many tests set.
const (
	setApps        = "apps.enabled=true"
	setTenantClaim = "apps.claimCheck.tenantClaim=tenant"
)

// The chart installs the App parts only if apps.enabled is true. The default install has no App
// CRD, no App grant and no App controller.

func TestAppsOnlyIfEnabled(t *testing.T) {
	const flag, role = "--apps-enabled", "koodam-app-manager-role"
	off, err := helmTemplate(t, "")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, off)
	}
	for _, s := range []string{flag, role, "appversions.core.koodam.dev", "httproutes", "--egress-proxy-namespace",
		"ValidatingAdmissionPolicy"} {
		if strings.Contains(off, s) {
			t.Errorf("the default install has %q", s)
		}
	}

	on, err := helmTemplate(t, "", setApps)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	crds := objects(t, on, "CustomResourceDefinition")
	if len(crds) != 3 {
		t.Fatalf("got %d CRDs in the templates, want the App, the AppVersion and the TenantQuota", len(crds))
	}
	for _, crd := range crds {
		if !strings.Contains(crd, "helm.sh/resource-policy: keep") {
			t.Error("an App CRD does not have the keep policy")
		}
	}
	const policy = "ValidatingAdmissionPolicy"
	if len(objects(t, on, policy)) != 1 || len(objects(t, on, policy+"Binding")) != 1 {
		t.Error("the install with apps.enabled does not have the admission policy and its binding")
	}
	const binding = "ClusterRoleBinding"
	if !strings.Contains(on, flag) || len(objects(t, on, binding)) <= len(objects(t, off, binding)) {
		t.Error("the install with apps.enabled has no App controller or no App role binding")
	}
	// With no egress proxy, the manager gets no selector of the proxy pods.
	if strings.Contains(on, "--egress-proxy-namespace") {
		t.Error("the manager has --egress-proxy-namespace with no egress proxy")
	}
}

// With the egress proxy, the manager gets the selector of the proxy pods, and the policy of the
// proxy permits the App pods.
func TestAppsWithEgressProxy(t *testing.T) {
	set := []string{"egressProxy.enabled=true", "egressProxy.clusterIP=10.96.0.50",
		"egressProxy.networkPolicy.apiServer[0].ports[0].port=6443"}
	off, err := helmTemplate(t, "", set...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, off)
	}
	if strings.Contains(off, "koodam.dev/app\n") {
		t.Error("the policy of the proxy permits App pods with apps.enabled false")
	}
	// The proxy gives an App pod an identity only in a named namespace, so the chart refuses the
	// Apps with the proxy and no namespace.
	apps := slices.Concat(set, []string{setApps, "apps.ingressNamespaces={gateway-system,edge}"})
	if out, err := helmTemplate(t, "", apps...); err == nil ||
		!strings.Contains(out, "workspace.networkPolicy.namespaces must have each namespace of the Apps") {
		t.Errorf("the Apps with the proxy and no namespace are accepted: %v", err)
	}
	on, err := helmTemplate(t, "", slices.Concat(apps, []string{"workspace.networkPolicy.namespaces={shop}"})...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	if !strings.Contains(on, `"--namespaces=shop"`) {
		t.Error("the proxy does not watch the namespace of the Apps")
	}
	for _, s := range []string{"--egress-proxy-namespace=koodam-system",
		"--egress-proxy-pod-labels=app.kubernetes.io/component=egress-proxy,app.kubernetes.io/instance=koodam," +
			"app.kubernetes.io/name=koodam", "key: koodam.dev/app\n",
		"--app-ingress-namespaces=gateway-system,edge"} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with apps.enabled and the egress proxy does not have %q", s)
		}
	}
}

// The claim check of the Apps is off by default. With an issuer, the manager gets the claim flags
// and the JWKS file, and a new JWKS starts the manager again.
func TestAppClaimCheck(t *testing.T) {
	const flag, jwks = "--app-claim-issuer", `{"keys":[{"kty":"EC"}]}`
	set := []string{setApps, "apps.claimCheck.issuer=https://issuer.example.com",
		"apps.claimCheck.audience=shop", setTenantClaim}
	// The claim check needs the egress proxy.
	const proxy = "egressProxy: {enabled: true, clusterIP: 10.96.0.50, networkPolicy: {enabled: false}}\n" +
		"workspace: {networkPolicy: {namespaces: [shop]}}\n"
	off, err := helmTemplate(t, "", setApps)
	if err != nil || strings.Contains(off, flag) || strings.Contains(off, "app-claim-jwks") {
		t.Fatalf("the install with no issuer has the claim check: %v", err)
	}
	// The issuer has no effect while the Apps are off.
	if out, err := helmTemplate(t, "", set[1:]...); err != nil || strings.Contains(out, flag) {
		t.Errorf("the install with no Apps has the claim check: %v", err)
	}
	if out, err := helmTemplate(t, proxy, set...); err == nil ||
		!strings.Contains(out, "apps.claimCheck.jwks is necessary") {
		t.Errorf("an issuer with no JWKS is accepted: %v", err)
	}
	if out, err := helmTemplate(t, proxy, set[0], set[1], "apps.claimCheck.jwks=x"); err == nil ||
		!strings.Contains(out, "apps.claimCheck.audience is necessary") {
		t.Errorf("an issuer with no audience is accepted: %v", err)
	}
	values := "apps:\n  claimCheck:\n    jwks: '" + jwks + "'\n"
	// With no egress proxy, a version has no NetworkPolicy, and a pod of the cluster can call the
	// app container with no claim. The chart refuses the issuer.
	if out, err := helmTemplate(t, values, set...); err == nil ||
		!strings.Contains(out, "apps.claimCheck needs egressProxy.enabled") {
		t.Errorf("an issuer with no egress proxy is accepted: %v", err)
	}
	on, err := helmTemplate(t, proxy+values, set...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	for _, s := range []string{`"--app-claim-issuer=https://issuer.example.com"`, `"--app-claim-audience=shop"`,
		`"--app-claim-caller-claim=sub"`, `"--app-claim-tenant-claim=tenant"`,
		"--app-claim-jwks-file=/etc/koodam/app-claim/jwks.json", "checksum/app-claim-jwks:", "kty"} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with an issuer does not have %q", s)
		}
	}
}

// With a claim check and a tenant claim, the tenant token is on by default. The manager and the
// proxy get their flags together, and the proxy can read the AppVersions of each App namespace.
func TestAppTenantToken(t *testing.T) {
	const values = "egressProxy: {enabled: true, clusterIP: 10.96.0.50, networkPolicy: {enabled: false}}\n" +
		"workspace: {networkPolicy: {namespaces: [shop, bank]}}\n" +
		"apps:\n  claimCheck:\n    jwks: '{\"keys\":[{\"kty\":\"EC\"}]}'\n"
	base := []string{setApps, "apps.claimCheck.issuer=https://issuer.example.com", "apps.claimCheck.audience=shop"}
	for name, c := range map[string]struct {
		set []string
		on  bool
	}{
		"a tenant claim":                     {[]string{setTenantClaim}, true},
		"no tenant claim":                    {nil, false},
		"a tenant claim with the switch off": {[]string{setTenantClaim, "apps.claimCheck.tenantToken=false"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, values, append(base, c.set...)...)
			if err != nil {
				t.Fatalf("helm template failed: %v\n%s", err, out)
			}
			if strings.Contains(out, `"--app-claim-tenant-token=true"`) != c.on ||
				strings.Contains(out, `"--app-claim-tenant-token=false"`) == c.on {
				t.Errorf("the manager flag of the tenant token must be %v", c.on)
			}
			deps := decode[appsv1.Deployment](t, out, "Deployment", proxyName)
			if slices.Contains(deps[0].Spec.Template.Spec.Containers[0].Args, "--tenant-tokens") != c.on {
				t.Errorf("the proxy flag --tenant-tokens must be %v", c.on)
			}
			roles := decode[rbacv1.Role](t, out, "Role", proxyName)
			if len(roles) != 2 {
				t.Fatalf("got %d proxy Roles, want 2", len(roles))
			}
			for _, r := range roles {
				if !c.on {
					checkPodRules(t, r.Rules)
					continue
				}
				checkPodRules(t, r.Rules[:1])
				if len(r.Rules) != 2 || !slices.Equal(r.Rules[1].APIGroups, []string{"core.koodam.dev"}) ||
					!slices.Equal(r.Rules[1].Resources, []string{"appversions"}) ||
					!slices.Equal(r.Rules[1].Verbs, []string{"get", "list", "watch"}) {
					t.Errorf("the proxy Role in %s must have get, list and watch on appversions: %+v", r.Namespace, r.Rules)
				}
			}
		})
	}
	// With no Apps, the proxy has no tenant token flag and no AppVersion rule.
	out := render(t, proxyValues, "workspace.networkPolicy.namespaces={dev-a}", setTenantClaim,
		"apps.claimCheck.issuer=https://issuer.example.com")
	if strings.Contains(out, "--tenant-tokens") || strings.Contains(out, "appversions") {
		t.Error("the install with no Apps has the tenant token of the proxy")
	}
}
