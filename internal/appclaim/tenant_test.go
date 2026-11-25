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
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	tenantFast = "fast"
	tenantFull = "full"
	tenantFree = "free"
)

func mustLimits(t *testing.T, doc string) *Tenants {
	t.Helper()
	limits, err := ParseTenantLimits([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	tenants := &Tenants{}
	tenants.limits.Store(limits)
	return tenants
}

func TestParseTenantLimits(t *testing.T) {
	for _, doc := range []string{
		`{}`,
		`{"refuseUnknown":true,"default":{"rate":0.5,"burst":1},"tenants":{"a":{"rate":2,"burst":4,"exceeded":true},"b":{}}}`,
	} {
		if _, err := ParseTenantLimits([]byte(doc)); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
	for _, doc := range []string{
		``, `[]`, `null`, ` null `, `{} {}`, `{}}`, `{}]`, `{} x`, `{"other":1}`,
		`{"default":{"rate":-1,"burst":1}}`,
		`{"default":{"rate":1}}`,
		`{"default":{"burst":3}}`,
		`{"tenants":{"a":{"rate":1,"burst":0}}}`,
		`{"tenants":{"a":{"rate":1000001,"burst":1}}}`,
		`{"tenants":{"a":{"rate":1,"burst":1000001}}}`,
		`{"tenants":{"":{}}}`,
		`{"tenants":{"` + strings.Repeat("a", maxLabel+1) + `":{}}}`,
		`{"tenants":{"a":{"rate":1,"burst":1,"more":1}}}`,
	} {
		if _, err := ParseTenantLimits([]byte(doc)); err == nil {
			t.Errorf("%s is accepted", doc)
		}
	}
}

func TestTenantsLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenants.json")
	tenants := &Tenants{}
	if tenants.Load(path) == nil {
		t.Fatal("A file that does not exist must give an error.")
	}
	if reason, _ := tenants.Allow(tenantFast, time.Unix(0, 0)); reason != "" {
		t.Errorf("no limits: %q", reason)
	}
	if err := os.WriteFile(path, []byte(`{"refuseUnknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tenants.Load(path); err != nil {
		t.Fatal(err)
	}
	// A file that is not valid, or too large, keeps the last valid limits. The JSON value null
	// is not a limits file.
	for _, bad := range [][]byte{[]byte("{"), []byte("null"), bytes.Repeat([]byte(" "), maxLimitsFile+1)} {
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if tenants.Load(path) == nil {
			t.Error("A bad file must give an error.")
		}
	}
	if reason, _ := tenants.Allow(tenantFast, time.Unix(0, 0)); reason != ReasonUnknownTenant {
		t.Errorf("after a bad reload: %q", reason)
	}
}

func TestTenantsAllow(t *testing.T) {
	tenants := mustLimits(t, `{"default":{"rate":1,"burst":1},
		"tenants":{"fast":{"rate":2,"burst":3},"full":{"rate":2,"burst":3,"exceeded":true},"free":{}}}`)
	at := func(ms int) time.Time { return time.Unix(100, 0).Add(time.Duration(ms) * time.Millisecond) }
	allow := func(tenant string, ms int) (string, time.Duration) { return tenants.Allow(tenant, at(ms)) }

	// The burst passes, then the bucket is empty.
	for i := range 3 {
		if reason, _ := allow(tenantFast, 0); reason != "" {
			t.Fatalf("request %d of the burst: %q", i, reason)
		}
	}
	if reason, wait := allow(tenantFast, 0); reason != ReasonTenantRateLimited || wait != 500*time.Millisecond {
		t.Errorf("empty bucket: %q, wait %v", reason, wait)
	}
	// A refused request uses no token: the wait gets smaller with the time only.
	if reason, wait := allow(tenantFast, 100); reason != ReasonTenantRateLimited || wait != 400*time.Millisecond {
		t.Errorf("after 100 ms: %q, wait %v", reason, wait)
	}
	// After 500 ms the bucket has 1 token again.
	if reason, _ := allow(tenantFast, 500); reason != "" {
		t.Errorf("after 500 ms: %q", reason)
	}
	if reason, _ := allow(tenantFast, 500); reason != ReasonTenantRateLimited {
		t.Errorf("second request after 500 ms: %q", reason)
	}
	// A clock that goes back adds no token.
	if reason, _ := allow(tenantFast, 0); reason != ReasonTenantRateLimited {
		t.Errorf("clock back: %q", reason)
	}
	// Requests with times that are not in order get the tokens of a time range one time only.
	// The default limit is 1 for each second with a burst of 1.
	passed := 0
	for _, ms := range []int{0, 1000, 0, 1000} {
		if reason, _ := allow("late", ms); reason == "" {
			passed++
		}
	}
	if passed != 2 {
		t.Errorf("times not in order: %d requests passed, want 2", passed)
	}
	// A long idle time fills the bucket to the burst, and not more.
	for i := range 3 {
		if reason, _ := allow(tenantFast, 60_000); reason != "" {
			t.Fatalf("request %d after the idle time: %q", i, reason)
		}
	}
	if reason, _ := allow(tenantFast, 60_000); reason != ReasonTenantRateLimited {
		t.Errorf("request 4 after the idle time: %q", reason)
	}

	// The limit of one tenant has no effect on a different tenant.
	if reason, _ := allow(tenantFree, 60_000); reason != "" {
		t.Errorf("tenant with no limit: %q", reason)
	}
	// A tenant at its quota is refused before the rate limit, and it uses no token.
	if reason, wait := allow(tenantFull, 0); reason != ReasonTenantQuotaExceeded || wait != 0 {
		t.Errorf("tenant at its quota: %q, wait %v", reason, wait)
	}
	// A tenant label with no entry gets the default limit, with its own bucket.
	if reason, _ := allow("new-a", 0); reason != "" {
		t.Errorf("default, first request: %q", reason)
	}
	if reason, wait := allow("new-a", 0); reason != ReasonTenantRateLimited || wait != time.Second {
		t.Errorf("default, second request: %q, wait %v", reason, wait)
	}
	if reason, _ := allow("new-b", 0); reason != "" {
		t.Errorf("default, second tenant: %q", reason)
	}

	// A reload with a smaller burst makes the bucket smaller.
	tenants.limits.Store(mustLimits(t, `{"tenants":{"fast":{"rate":2,"burst":1}}}`).limits.Load())
	if reason, _ := allow(tenantFast, 120_000); reason != "" {
		t.Errorf("after the reload: %q", reason)
	}
	if reason, _ := allow(tenantFast, 120_000); reason != ReasonTenantRateLimited {
		t.Errorf("second request after the reload: %q", reason)
	}
	// With no default, an unknown tenant has no limit. With refuseUnknown, it is refused.
	if reason, _ := allow("new-a", 120_000); reason != "" {
		t.Errorf("no default: %q", reason)
	}
	tenants.limits.Store(mustLimits(t, `{"refuseUnknown":true,"default":{"rate":1,"burst":1},"tenants":{"free":{}}}`).limits.Load())
	if reason, _ := allow("new-a", 120_000); reason != ReasonUnknownTenant {
		t.Errorf("refuseUnknown: %q", reason)
	}
	if reason, _ := allow(tenantFree, 120_000); reason != "" {
		t.Errorf("refuseUnknown, known tenant: %q", reason)
	}
}

func TestTenantsSmallRateAndRemoval(t *testing.T) {
	tenants := mustLimits(t, `{"default":{"rate":1e-12,"burst":1}}`)
	start := time.Unix(100, 0)
	if reason, _ := tenants.Allow("a", start); reason != "" {
		t.Fatal(reason)
	}
	if reason, wait := tenants.Allow("a", start); reason != ReasonTenantRateLimited || wait != maxTenantWait {
		t.Errorf("small rate: %q, wait %v", reason, wait)
	}
	// At the bucket limit, the check removes the full buckets and keeps the buckets in use.
	tenants = mustLimits(t, `{"default":{"rate":1,"burst":1},"tenants":{"free":{}}}`)
	for i := range maxTenantBuckets - 1 {
		tenants.Allow("idle-"+string(rune('a'+i%26))+strings.Repeat("x", i/26), start)
	}
	later := start.Add(time.Minute)
	tenants.Allow("busy", later)
	if len(tenants.buckets) != maxTenantBuckets {
		t.Fatalf("got %d buckets before the removal", len(tenants.buckets))
	}
	tenants.Allow("next", later)
	if len(tenants.buckets) != 2 || tenants.buckets["busy"] == nil || tenants.buckets["next"] == nil {
		t.Errorf("got %d buckets after the removal", len(tenants.buckets))
	}
	if reason, _ := tenants.Allow("busy", later); reason != ReasonTenantRateLimited {
		t.Errorf("a bucket in use lost its state: %q", reason)
	}
}

// If no bucket is full, a removal does not start again for each new tenant. The next removal
// starts at two times the count of the buckets in use.
func TestTenantsRemovalWorkHasALimit(t *testing.T) {
	tenants := mustLimits(t, `{"default":{"rate":1e-12,"burst":1}}`)
	start := time.Unix(100, 0)
	name := func(i int) string { return "t-" + strconv.Itoa(i) }
	for i := range maxTenantBuckets {
		tenants.Allow(name(i), start)
	}
	if tenants.removeAt != 0 {
		t.Fatalf("a removal started below the limit: %d", tenants.removeAt)
	}
	for i := maxTenantBuckets; i < 2*maxTenantBuckets; i++ {
		tenants.Allow(name(i), start)
		if tenants.removeAt != 2*maxTenantBuckets {
			t.Fatalf("with %d buckets in use, the next removal is at %d", i+1, tenants.removeAt)
		}
	}
	tenants.Allow(name(2*maxTenantBuckets), start)
	if tenants.removeAt != 4*maxTenantBuckets || len(tenants.buckets) != 2*maxTenantBuckets+1 {
		t.Errorf("next removal at %d with %d buckets", tenants.removeAt, len(tenants.buckets))
	}
	// A removal that makes the map small moves the start back to the minimum.
	tenants.limits.Store(&TenantLimits{Default: &TenantLimit{Rate: 1, Burst: 1}})
	tenants.removeAt = len(tenants.buckets)
	tenants.Allow("last", start.Add(time.Minute))
	if tenants.removeAt != 0 || len(tenants.buckets) != 1 {
		t.Errorf("after a full removal: next removal at %d with %d buckets", tenants.removeAt, len(tenants.buckets))
	}
}

// A request with an older time makes a removed bucket again. The tenant does not get the tokens
// of the time before the removal a second time.
func TestTenantsRemovalKeepsTheTime(t *testing.T) {
	tenants := mustLimits(t, `{"default":{"rate":1,"burst":1}}`)
	start := time.Unix(100, 0)
	later := start.Add(time.Second)
	accepted := 0
	if reason, _ := tenants.Allow("late", start); reason == "" {
		accepted++
	}
	for i := range maxTenantBuckets - 1 {
		tenants.Allow("idle-"+string(rune('a'+i%26))+strings.Repeat("x", i/26), start)
	}
	tenants.Allow("next", later)
	if tenants.buckets["late"] != nil {
		t.Fatal("the removal kept a full bucket")
	}
	for _, at := range []time.Time{start, later, later} {
		if reason, _ := tenants.Allow("late", at); reason == "" {
			accepted++
		}
	}
	if accepted != 2 {
		t.Errorf("got %d accepted requests in 1 second for rate 1 and burst 1, want 2", accepted)
	}
}

// The handler refuses a tenant with a typed reason, and the app does not get the request.
func TestCheckTenantRefusals(t *testing.T) {
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
	check := &Check{Config: testCfg, Keys: keys, Spans: NewSpanWriter(spans), App: testAud, Version: "shop-v2",
		Now:  func() time.Time { return now },
		Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })}
	token := f.sign(t, header(algES256, "e1"), claims(nil))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		check.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		limits, reason, retry string
		status, passed        int
	}{
		{limits: `{"tenants":{"` + testTenant + `":{"rate":0.5,"burst":1}}}`, reason: ReasonTenantRateLimited,
			status: http.StatusTooManyRequests, retry: "2", passed: 1},
		{limits: `{"tenants":{"` + testTenant + `":{"exceeded":true}}}`, reason: ReasonTenantQuotaExceeded,
			status: http.StatusTooManyRequests},
		{limits: `{"refuseUnknown":true,"tenants":{"other":{}}}`, reason: ReasonUnknownTenant,
			status: http.StatusForbidden},
	} {
		check.Tenants = mustLimits(t, tc.limits)
		calls = 0
		spans.Reset()
		var w *httptest.ResponseRecorder
		for range tc.passed + 1 {
			w = call()
		}
		if w.Code != tc.status || w.Header().Get(ReasonHeader) != tc.reason || w.Header().Get("Retry-After") != tc.retry ||
			strings.TrimSpace(w.Body.String()) != tc.reason || calls != tc.passed {
			t.Errorf("%s: status %d, headers %v, body %q, %d requests to the app", tc.reason, w.Code, w.Header(),
				w.Body.String(), calls)
		}
		lines := strings.Split(strings.TrimSpace(spans.String()), "\n")
		want := `"outcome":"` + tc.reason + `","caller":"` + testCaller + `","tenant":"` + testTenant + `"`
		if last := lines[len(lines)-1]; !strings.Contains(last, want) ||
			!strings.Contains(last, `"status":`+strconv.Itoa(tc.status)+`,`) || strings.Contains(spans.String(), token) {
			t.Errorf("%s: span %s", tc.reason, last)
		}
	}
	// A token with no tenant claim has no tenant limit.
	check.Config.TenantClaim = ""
	check.Tenants = mustLimits(t, `{"refuseUnknown":true}`)
	calls = 0
	if w := call(); w.Code != http.StatusOK || calls != 1 {
		t.Errorf("no tenant claim: status %d, %d requests to the app", w.Code, calls)
	}
}
