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

// The manager gets the zone and the namespaces of the install. The release namespace is always
// in the list, and it is in the list one time.
func TestZoneValues(t *testing.T) {
	const zoneFlag, watchFlag = "--zone=", "--watch-namespaces="
	off, err := helmTemplate(t, "")
	if err != nil || strings.Contains(off, zoneFlag) || strings.Contains(off, watchFlag) {
		t.Fatalf("the install with no zone has a zone flag: %v", err)
	}
	on, err := helmTemplate(t, "zone: {name: zone-a, namespaces: [team-1, koodam-system, team-2]}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	for _, s := range []string{`"` + zoneFlag + `zone-a"`, `"` + watchFlag + `koodam-system,team-1,team-2"`} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with a zone does not have %s", s)
		}
	}
	for _, name := range []string{"Zone_A", "zone a", "-zone", strings.Repeat("z", 64)} {
		if out, err := helmTemplate(t, "zone: {name: \""+name+"\"}\n"); err == nil ||
			!strings.Contains(out, "zone.name must be a DNS label") {
			t.Errorf("the zone name %q is accepted: %v", name, err)
		}
	}
}

// The manager gets the zone claim of the claim check. The chart refuses a zone claim with no zone
// and a necessary zone claim with no claim check.
func TestZoneClaimValues(t *testing.T) {
	const claimFlag, requireFlag = "--app-claim-zone-claim=", "--app-claim-require-zone"
	const claim = "egressProxy: {enabled: true, clusterIP: 10.96.0.50, networkPolicy: {enabled: false}}\n" +
		"workspace: {networkPolicy: {namespaces: [shop]}}\n" +
		"apps:\n  enabled: true\n  claimCheck:\n    issuer: https://issuer.example.com\n    audience: shop\n" +
		"    jwks: '{\"keys\":[{\"kty\":\"EC\"}]}'\n"
	const zoneClaim, zone = "    zoneClaim: zone\n", "zone: {name: zone-a}\n"
	plain, err := helmTemplate(t, claim+zone)
	if err != nil || strings.Contains(plain, claimFlag) || strings.Contains(plain, requireFlag) {
		t.Fatalf("the install with no zone claim has a zone claim flag: %v", err)
	}
	on, err := helmTemplate(t, claim+zoneClaim+"zone: {name: zone-a, requireClaim: true}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	for _, s := range []string{`"` + claimFlag + `zone"`, "- " + requireFlag + "\n", `"--zone=zone-a"`} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with a zone claim does not have %q", s)
		}
	}
	if out, err := helmTemplate(t, claim+zoneClaim); err == nil ||
		!strings.Contains(out, "apps.claimCheck.zoneClaim needs zone.name") {
		t.Errorf("a zone claim with no zone is accepted: %v", err)
	}
	for name, values := range map[string]string{
		"no Apps":       "zone: {name: zone-a, requireClaim: true}\n",
		"no issuer":     "apps: {enabled: true, claimCheck: {zoneClaim: zone}}\nzone: {name: zone-a, requireClaim: true}\n",
		"no zone claim": claim + "zone: {name: zone-a, requireClaim: true}\n",
	} {
		if out, err := helmTemplate(t, values); err == nil || !strings.Contains(out, "zone.requireClaim needs") {
			t.Errorf("%s: a necessary zone claim is accepted: %v", name, err)
		}
	}
}

// The route list of the gateway has only the model routes of the zone of the install. The zone
// hook gets the model names that only routes of a different zone have.
func TestZoneRoutes(t *testing.T) {
	const hookKey, hookSetting = "koodam_zone.py: |", "callbacks: koodam_zone.handler"
	const listEnv = "KOODAM_OUT_OF_ZONE_MODELS"
	const routes = "gateway:\n  bundled:\n    litellm:\n      modelList:\n" +
		"      - {model_name: model-a, zone: zone-a, litellm_params: {model: openai/route-a}}\n" +
		"      - {model_name: model-b, zone: zone-b, litellm_params: {model: openai/route-other}}\n" +
		"      - {model_name: shared, litellm_params: {model: openai/route-shared}}\n" +
		"      - {model_name: both, zone: zone-a, litellm_params: {model: openai/route-both-a}}\n" +
		"      - {model_name: both, zone: zone-b, litellm_params: {model: openai/route-both-b}}\n" +
		"      - {model_name: \"odd,name\", zone: zone-c, litellm_params: {model: openai/route-c}}\n"
	// An install with no zone makes no zone check: each route is in the list, with no zone key.
	all, err := helmTemplate(t, routes)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, all)
	}
	for _, s := range []string{"route-a", "route-other", "route-shared", "route-both-a", "route-both-b", "route-c"} {
		if !strings.Contains(all, "openai/"+s) {
			t.Errorf("the install with no zone does not have %s", s)
		}
	}
	for _, s := range []string{hookKey, hookSetting, listEnv, "zone: zone-"} {
		if strings.Contains(all, s) {
			t.Errorf("the install with no zone has %q", s)
		}
	}
	in, err := helmTemplate(t, routes+"zone: {name: zone-a}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, in)
	}
	for _, s := range []string{"openai/route-a", "openai/route-shared", "openai/route-both-a", hookKey, hookSetting,
		`value: "[\"model-b\",\"odd,name\"]"`, "name: KOODAM_ZONE\n          value: \"zone-a\"",
		"async def async_pre_call_hook"} {
		if !strings.Contains(in, s) {
			t.Errorf("the install of zone-a does not have %q", s)
		}
	}
	for _, s := range []string{"route-other", "route-both-b", "route-c", "zone: zone-"} {
		if strings.Contains(in, s) {
			t.Errorf("the install of zone-a has %q", s)
		}
	}
	// With only routes of the zone, the gateway has no hook.
	own, err := helmTemplate(t, "zone: {name: zone-a}\ngateway: {bundled: {litellm: {modelList: "+
		"[{model_name: model-a, zone: zone-a, litellm_params: {model: openai/route-a}}]}}}\n")
	if err != nil || strings.Contains(own, hookKey) || strings.Contains(own, listEnv) ||
		!strings.Contains(own, "openai/route-a") {
		t.Errorf("the install with only its own routes: %v", err)
	}
	// The mock route is a route of the install. Its model name is not in the refusal list, also
	// if only a route of a different zone has the same name.
	mock, err := helmTemplate(t, routes+"zone: {name: zone-a}\n",
		"gateway.bundled.mock.enabled=true", "gateway.bundled.mock.modelName=model-b")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, mock)
	}
	for _, s := range []string{`model: "openai/model-b"`, `value: "[\"odd,name\"]"`} {
		if !strings.Contains(mock, s) {
			t.Errorf("the install with the mock route does not have %q", s)
		}
	}
	// With the mock name as the only name of a different zone, the gateway has no hook.
	only, err := helmTemplate(t, "zone: {name: zone-a}\ngateway: {bundled: {mock: {enabled: true, modelName: model-b}, "+
		"litellm: {modelList: [{model_name: model-b, zone: zone-b, litellm_params: {model: openai/route-other}}]}}}\n")
	if err != nil || strings.Contains(only, hookKey) || strings.Contains(only, listEnv) {
		t.Errorf("the install with only the mock name out of the zone has the hook: %v", err)
	}
	for name, tc := range map[string][2]string{
		"zone not a string": {"[{model_name: model-a, zone: 7}]", "modelList[0].zone must be a string"},
		"no model name":     {"[{zone: zone-b}]", "modelList[0].model_name is necessary"},
	} {
		out, err := helmTemplate(t, "zone: {name: zone-a}\ngateway: {bundled: {litellm: {modelList: "+tc[0]+"}}}\n")
		if err == nil || !strings.Contains(out, tc[1]) {
			t.Errorf("%s is accepted: %v", name, err)
		}
	}
	for _, env := range []string{"KOODAM_ZONE", listEnv} {
		out, err := helmTemplate(t, "gateway: {bundled: {litellm: {extraEnvFromSecrets: "+
			"[{name: "+env+", secretName: s, secretKey: k}]}}}\n")
		if err == nil || !strings.Contains(out, "must not be "+env) {
			t.Errorf("a Secret can set %s: %v", env, err)
		}
	}
}

// The App CRDs and the admission policy of the AppVersion are for the full cluster. The second
// install of a cluster does not render them, and it has no other object for the full cluster
// with a name that the first install has.
func TestSharedResourcesOff(t *testing.T) {
	const crd, policy = "kind: CustomResourceDefinition", "kind: ValidatingAdmissionPolicy"
	first, err := helmTemplate(t, "apps: {enabled: true}\n")
	if err != nil || !strings.Contains(first, crd) || !strings.Contains(first, policy) {
		t.Fatalf("the first install does not have the shared resources: %v", err)
	}
	second, err := helmTemplate(t, "apps: {enabled: true, sharedResources: false}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, second)
	}
	if strings.Contains(second, crd) || strings.Contains(second, policy) {
		t.Error("the second install has a shared resource")
	}
	if !strings.Contains(second, "app-manager-role") {
		t.Error("the second install does not have the role of the App controllers")
	}
}
