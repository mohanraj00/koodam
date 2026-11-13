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
	"strings"
	"testing"
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

	on, err := helmTemplate(t, "", "apps.enabled=true")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	crds := objects(t, on, "CustomResourceDefinition")
	if len(crds) != 2 {
		t.Fatalf("got %d CRDs in the templates, want the App and the AppVersion", len(crds))
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
	on, err := helmTemplate(t, "", append(set, "apps.enabled=true", "apps.ingressNamespaces={gateway-system,edge}")...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
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
