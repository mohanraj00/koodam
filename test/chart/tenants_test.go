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

// The manager gets the read interval of the tenant use with the Apps. The values for a tenant
// label with no TenantQuota need the claim check with a tenant claim.
func TestTenantValues(t *testing.T) {
	const interval, rate, burst, refuse = "--tenant-usage-interval", "--tenant-default-rate", "--tenant-default-burst",
		"--tenant-refuse-unknown"
	off, err := helmTemplate(t, "")
	if err != nil || strings.Contains(off, "--tenant-") {
		t.Fatalf("the install with no Apps has a tenant flag: %v", err)
	}
	apps, err := helmTemplate(t, "", "apps.enabled=true", "tenants.usageInterval=2m")
	if err != nil || !strings.Contains(apps, `"`+interval+`=2m"`) {
		t.Fatalf("the install with the Apps does not have the read interval: %v", err)
	}
	for _, s := range []string{rate, burst, refuse} {
		if strings.Contains(apps, s) {
			t.Errorf("the default install with the Apps has %q", s)
		}
	}
	const claim = "egressProxy: {enabled: true, clusterIP: 10.96.0.50, networkPolicy: {enabled: false}}\n" +
		"workspace: {networkPolicy: {namespaces: [shop]}}\n" +
		"apps:\n  enabled: true\n  claimCheck:\n    issuer: https://issuer.example.com\n    audience: shop\n" +
		"    jwks: '{\"keys\":[{\"kty\":\"EC\"}]}'\n"
	const tenantClaim = "    tenantClaim: tenant\n"
	for name, values := range map[string]string{
		"refuseUnknown": "tenants: {refuseUnknown: true}\n",
		"rate":          "tenants: {defaultQuota: {rateLimit: {requestsPerSecond: \"5\"}}}\n",
		"burst":         "tenants: {defaultQuota: {rateLimit: {burst: 3}}}\n",
	} {
		// With no claim check, and with a claim check that has no tenant claim, the chart refuses the value.
		for _, base := range []string{"apps: {enabled: true}\n", claim} {
			if out, err := helmTemplate(t, base+values); err == nil ||
				!strings.Contains(out, "need apps.claimCheck.issuer and apps.claimCheck.tenantClaim") {
				t.Errorf("%s with no tenant claim is accepted: %v", name, err)
			}
		}
	}
	on, err := helmTemplate(t, claim+tenantClaim+
		"tenants: {refuseUnknown: true, defaultQuota: {rateLimit: {requestsPerSecond: 500m, burst: 3}}}\n")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, on)
	}
	for _, s := range []string{`"` + rate + `=500m"`, `"` + burst + `=3"`, refuse, `"` + interval + `=30s"`} {
		if !strings.Contains(on, s) {
			t.Errorf("the install with the tenant values does not have %q", s)
		}
	}
}
