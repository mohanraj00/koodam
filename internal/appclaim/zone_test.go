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

package appclaim

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	zoneHere, zoneOther = "zone-a", "zone-b"
	zoneKey             = "zone"
)

func zoneCfg(require bool) Config {
	cfg := testCfg
	cfg.Zone, cfg.ZoneClaim, cfg.RequireZoneClaim = zoneHere, zoneKey, require
	return cfg
}

// A zone claim that is there must be the zone of the install. A token with no zone claim is a
// mismatch only if the claim is necessary.
func TestZoneMismatch(t *testing.T) {
	f := newFixture(t)
	for name, tc := range map[string]struct {
		zone     any
		require  bool
		mismatch bool
	}{
		"the zone of the install":     {zone: zoneHere},
		"a different zone":            {zone: zoneOther, mismatch: true},
		"a different case":            {zone: "Zone-A", mismatch: true},
		"an empty zone":               {zone: "", mismatch: true},
		"a number":                    {zone: 7, mismatch: true},
		"a list with the zone":        {zone: []string{zoneHere}, mismatch: true},
		"no claim":                    {},
		"no claim, necessary":         {require: true, mismatch: true},
		"the install zone, necessary": {zone: zoneHere, require: true},
		"a different zone, necessary": {zone: zoneOther, require: true, mismatch: true},
		"a null claim":                {zone: nullClaim{}, mismatch: true},
		"a null claim, necessary":     {zone: nullClaim{}, require: true, mismatch: true},
	} {
		token := f.sign(t, header(algES256, "e1"), claims(func(c map[string]any) {
			if tc.zone != nil {
				c[zoneKey] = tc.zone
			}
		}))
		id, err := zoneCfg(tc.require).Verify(f.keys, token, now)
		want := Identity{Caller: testCaller, Tenant: testTenant, ZoneMismatch: tc.mismatch}
		if err != nil || id != want {
			t.Errorf("%s: got %+v and %v, want %+v", name, id, err, want)
		}
	}
	// With no zone claim in the configuration, the check reads no zone.
	token := f.sign(t, header(algES256, "e1"), claims(func(c map[string]any) { c[zoneKey] = zoneOther }))
	if id, err := testCfg.Verify(f.keys, token, now); err != nil || id.ZoneMismatch {
		t.Errorf("a check with no zone claim: %+v, %v", id, err)
	}
}

// nullClaim is a claim with the JSON value null.
type nullClaim struct{}

func (nullClaim) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

func TestZoneConfigValid(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*Config)
		valid  bool
	}{
		"a zone and no claim":       {change: func(c *Config) { c.Zone = zoneHere }, valid: true},
		"a zone and a claim":        {change: func(c *Config) { c.Zone, c.ZoneClaim = zoneHere, zoneKey }, valid: true},
		"a claim and no zone":       {change: func(c *Config) { c.ZoneClaim = zoneKey }},
		"necessary and no claim":    {change: func(c *Config) { c.Zone, c.RequireZoneClaim = zoneHere, true }},
		"necessary with all values": {change: func(c *Config) { *c = zoneCfg(true) }, valid: true},
	} {
		cfg := testCfg
		tc.change(&cfg)
		if err := cfg.Valid(); (err == nil) != tc.valid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A caller of a different zone gets 403 ZoneMismatch. The app gets no request, the tenant uses
// no token of its rate limit, and the span has the zone of the install.
func TestCheckRefusesZone(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, f.jwks, 0o600); err != nil {
		t.Fatal(err)
	}
	keys := &Keys{}
	if err := keys.Load(path); err != nil {
		t.Fatal(err)
	}
	calls := 0
	spans := &bytes.Buffer{}
	check := &Check{Config: zoneCfg(false), Keys: keys, Spans: NewSpanWriter(spans), App: testAud, Version: "shop-v3",
		Now:     func() time.Time { return now },
		Tenants: mustLimits(t, `{"tenants":{"`+testTenant+`":{"rate":0.001,"burst":1}}}`),
		Next:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })}
	call := func(zone string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+f.sign(t, header(algES256, "e1"),
			claims(func(c map[string]any) { c[zoneKey] = zone })))
		w := httptest.NewRecorder()
		check.ServeHTTP(w, r)
		return w
	}
	// The refused calls come first. If one of them used the token of the tenant, the last call
	// would get TenantRateLimited.
	for range 3 {
		w := call(zoneOther)
		if w.Code != http.StatusForbidden || w.Header().Get(ReasonHeader) != ReasonZoneMismatch ||
			strings.TrimSpace(w.Body.String()) != ReasonZoneMismatch || w.Header().Get("Retry-After") != "" {
			t.Fatalf("a different zone: status %d, headers %v, body %q", w.Code, w.Header(), w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("the app got %d requests of a different zone", calls)
	}
	want := `"status":403,"outcome":"ZoneMismatch","caller":"` + testCaller + `","tenant":"` + testTenant +
		`","zone":"` + zoneHere + `"`
	if got := spans.String(); strings.Count(got, want) != 3 || strings.Contains(got, zoneOther) {
		t.Errorf("the spans of the refusals: %s", got)
	}
	spans.Reset()
	if w := call(zoneHere); w.Code != http.StatusOK || calls != 1 {
		t.Errorf("the zone of the install: status %d, %d requests to the app", w.Code, calls)
	}
	if got := spans.String(); !strings.Contains(got, `"outcome":"ok"`) || !strings.Contains(got, `"zone":"`+zoneHere+`"`) {
		t.Errorf("the span of the accepted call: %s", got)
	}
}
