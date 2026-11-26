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
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// The typed reasons of a tenant refusal. They are the outcome and the reason of the span, and
// the reason in the body of the answer.
const (
	// ReasonTenantRateLimited tells that the tenant sent more requests than its rate limit.
	ReasonTenantRateLimited = "TenantRateLimited"
	// ReasonTenantQuotaExceeded tells that the controller marked the tenant at its limit of the
	// current period.
	ReasonTenantQuotaExceeded = "TenantQuotaExceeded"
	// ReasonUnknownTenant tells that the limits do not name the tenant label, and the install
	// refuses such a tenant.
	ReasonUnknownTenant = "UnknownTenant"
	// ReasonReservedTenant tells that the claim has the reserved tenant label of the calls with
	// no tenant. No tenant can have this label.
	ReasonReservedTenant = "ReservedTenant"
)

const (
	// maxTenantRate is the largest rate and the largest burst of a tenant.
	maxTenantRate = 1_000_000
	// maxTenantBuckets is the smallest count of buckets that starts a removal of the full
	// buckets.
	maxTenantBuckets = 4096
	// maxLimitsFile is the largest limits file. A ConfigMap has 1 MiB at most.
	maxLimitsFile = 1 << 20
	// maxTenantWait is the largest wait that Allow gives back.
	maxTenantWait = 24 * time.Hour
)

// TenantLimit is the limit of one tenant in one pod. The controller divides the rate and the
// burst of the tenant by the replicas of the version.
type TenantLimit struct {
	// Rate is the count of requests for each second. 0 gives no rate limit.
	Rate float64 `json:"rate,omitempty"`
	// Burst is the size of the token bucket. It is 1 or more if Rate is more than 0.
	Burst int `json:"burst,omitempty"`
	// Exceeded shows that the tenant is at its limit of the current period.
	Exceeded bool `json:"exceeded,omitempty"`
}

// TenantLimits is the limits file of the check container. The controller writes it.
type TenantLimits struct {
	// RefuseUnknown refuses a tenant label that Tenants does not name.
	RefuseUnknown bool `json:"refuseUnknown,omitempty"`
	// Default is the limit of a tenant label that Tenants does not name. With no value, such a
	// tenant has no limit.
	Default *TenantLimit `json:"default,omitempty"`
	// Tenants has the limit of each known tenant label.
	Tenants map[string]TenantLimit `json:"tenants,omitempty"`
}

// ParseTenantLimits reads a limits file. It refuses an unknown field, a rate that is not from 0
// to maxTenantRate, and a rate with a burst that is not from 1 to maxTenantRate.
func ParseTenantLimits(data []byte) (*TenantLimits, error) {
	// The JSON value null decodes to limits with no field. Only an object can be a limits file.
	if body := bytes.TrimSpace(data); len(body) == 0 || body[0] != '{' {
		return nil, errors.New("the tenant limits are not one JSON object of the known fields")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l TenantLimits
	if err := dec.Decode(&l); err != nil {
		return nil, errors.New("the tenant limits are not one JSON object of the known fields")
	}
	// Only the end of the data can follow the object. A second value or a delimiter is an error.
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("the tenant limits are not one JSON object of the known fields")
	}
	valid := func(t TenantLimit) bool {
		if math.IsNaN(t.Rate) || t.Rate < 0 || t.Rate > maxTenantRate {
			return false
		}
		if t.Rate == 0 {
			return t.Burst == 0
		}
		return t.Burst >= 1 && t.Burst <= maxTenantRate
	}
	if l.Default != nil && !valid(*l.Default) {
		return nil, errors.New("the default tenant limit is not valid")
	}
	for name, t := range l.Tenants {
		if name == "" || len(name) > maxLabel || !valid(t) {
			return nil, errors.New("a tenant limit is not valid")
		}
	}
	return &l, nil
}

// bucket is the token bucket of one tenant.
type bucket struct {
	tokens float64
	last   time.Time
}

// Tenants has the current limits and the token bucket of each tenant. A reload changes the
// limits with no restart, and it keeps the buckets.
type Tenants struct {
	limits  atomic.Pointer[TenantLimits]
	mu      sync.Mutex
	buckets map[string]*bucket
	// removed is the latest time of a bucket that removeIdle removed. A new bucket starts at
	// this time or later.
	removed time.Time
	// removeAt is the count of buckets that starts the next removal, if it is more than
	// maxTenantBuckets.
	removeAt int
}

// Load reads the limits file. If the file is not valid, the last valid limits stay.
func (t *Tenants) Load(path string) error {
	f, err := os.Open(path) //nolint:gosec // The operator configures the path.
	if err != nil {
		return errors.New("the tenant limits file is not readable")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLimitsFile+1))
	if err != nil || len(data) > maxLimitsFile {
		return errors.New("the tenant limits file is not readable or too large")
	}
	limits, err := ParseTenantLimits(data)
	if err != nil {
		return err
	}
	t.limits.Store(limits)
	return nil
}

// Allow examines one request of a tenant. It gives back an empty reason if the request can go
// to the app. If not, it gives back the typed reason, and for a rate limit the time until the
// tenant has the next token. A request that it refuses uses no token.
func (t *Tenants) Allow(tenant string, now time.Time) (reason string, wait time.Duration) {
	limits := t.limits.Load()
	if limits == nil {
		return "", 0
	}
	limit, known := limits.Tenants[tenant]
	if !known {
		if limits.RefuseUnknown {
			return ReasonUnknownTenant, 0
		}
		if limits.Default == nil {
			return "", 0
		}
		limit = *limits.Default
	}
	if limit.Exceeded {
		return ReasonTenantQuotaExceeded, 0
	}
	if limit.Rate <= 0 {
		return "", 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	size := float64(limit.Burst)
	b := t.buckets[tenant]
	if b == nil {
		if t.buckets == nil {
			t.buckets = map[string]*bucket{}
		}
		if len(t.buckets) >= max(t.removeAt, maxTenantBuckets) {
			t.removeIdle(limits, now)
			// A removal examines all the buckets with the lock. The next removal starts at two
			// times the count of the buckets in use. Thus the work of the removals stays in
			// proportion to the count of new buckets, also if no bucket is full.
			t.removeAt = 2 * len(t.buckets)
		}
		// A request with an older time can make the bucket again after a removal. The bucket
		// then starts at the time of the removal, and it adds no token for the time before.
		b = &bucket{tokens: size, last: now}
		if t.removed.After(now) {
			b.last = t.removed
		}
		t.buckets[tenant] = b
	}
	// The requests of one tenant can come with times that are not in order, and a clock can go
	// back. An older time adds no token, and it does not move the time of the bucket back. Thus
	// the bucket adds the tokens of a time range one time only.
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() * limit.Rate
		b.last = now
	}
	// A smaller burst after a reload also makes the bucket smaller.
	b.tokens = math.Min(b.tokens, size)
	if b.tokens < 1 {
		// The limit keeps the wait in the range of a duration for a very small rate.
		seconds := math.Min((1-b.tokens)/limit.Rate, maxTenantWait.Seconds())
		return ReasonTenantRateLimited, time.Duration(seconds * float64(time.Second))
	}
	b.tokens--
	return "", 0
}

// removeIdle removes each bucket that is full at this time, and each bucket of a tenant with no
// rate limit. A full bucket has the same state as a new bucket that starts at the time of the
// removal, so the removal changes no answer. The caller holds the lock.
func (t *Tenants) removeIdle(limits *TenantLimits, now time.Time) {
	for name, b := range t.buckets {
		limit, known := limits.Tenants[name]
		if !known && limits.Default != nil {
			limit = *limits.Default
		}
		if limit.Rate <= 0 || b.tokens+now.Sub(b.last).Seconds()*limit.Rate >= float64(limit.Burst) {
			delete(t.buckets, name)
			for _, at := range []time.Time{now, b.last} {
				if at.After(t.removed) {
					t.removed = at
				}
			}
		}
	}
}
