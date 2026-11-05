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

package ingress

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

func TestNewTargetResolver(t *testing.T) {
	f := newTargetWorld(t.Context())
	r := tgResolver(t, f)
	if len(r.guards) != 2 || r.guards[tgNamespace].Namespace != tgNamespace ||
		r.guards[tgOtherNamespace].Namespace != tgOtherNamespace {
		t.Fatal("the resolver does not have exactly the two configured run namespaces")
	}
	if len(f.ops) != 0 {
		t.Fatal("newTargetResolver read an object")
	}
}

func TestNewTargetResolverRefuses(t *testing.T) {
	g := tgGuard
	withLabel := func(k, v string) agentingress.PolicyGuard {
		x := tgGuard(tgOtherNamespace)
		x.ProxyLabels[k] = v
		return x
	}
	for _, tc := range []struct {
		name   string
		nilRdr bool
		guards []agentingress.PolicyGuard
	}{
		{"nil reader", true, []agentingress.PolicyGuard{g(tgNamespace)}},
		{"nil guards", false, nil},
		{"empty guards", false, []agentingress.PolicyGuard{}},
		{litCaseEmptyNS, false, []agentingress.PolicyGuard{g("")}},
		{"wildcard namespace", false, []agentingress.PolicyGuard{g("*")}},
		{"wildcard after a valid namespace", false, []agentingress.PolicyGuard{g(tgNamespace), g("*")}},
		{litCaseUpperNS, false, []agentingress.PolicyGuard{g("Runs")}},
		{"namespace with a dot", false, []agentingress.PolicyGuard{g("runs.a")}},
		{"duplicate namespace", false, []agentingress.PolicyGuard{g(tgNamespace), g(tgNamespace)}},
		{"invalid policy name", false, []agentingress.PolicyGuard{{Namespace: tgNamespace, Name: "Bad_Name",
			ProxyNamespace: tgProxyNamespace, ProxyLabels: tgProxyLabels()}}},
		{"invalid proxy labels", false, []agentingress.PolicyGuard{{Namespace: tgNamespace, Name: tgPolicyName,
			ProxyNamespace: tgProxyNamespace, ProxyLabels: map[string]string{"app": "x"}}}},
		{"different policy name", false, []agentingress.PolicyGuard{g(tgNamespace), {Namespace: tgOtherNamespace,
			Name: "other-agent", ProxyNamespace: tgProxyNamespace, ProxyLabels: tgProxyLabels()}}},
		{"different proxy namespace", false, []agentingress.PolicyGuard{g(tgNamespace), {Namespace: tgOtherNamespace,
			Name: tgPolicyName, ProxyNamespace: "other-system", ProxyLabels: tgProxyLabels()}}},
		{"different proxy labels", false, []agentingress.PolicyGuard{g(tgNamespace),
			withLabel(tgInstanceKey, tgOther)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			var rdr client.Reader = f
			if tc.nilRdr {
				rdr = nil
			}
			r, err := newTargetResolver(rdr, tc.guards)
			if r != nil || err != ErrTargetConfig {
				t.Fatal("newTargetResolver did not refuse with only ErrTargetConfig")
			}
			if len(f.ops) != 0 {
				t.Fatal("newTargetResolver read an object")
			}
		})
	}
}
