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
