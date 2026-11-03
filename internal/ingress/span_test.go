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
	"bytes"
	"crypto/rand"
	"math"
	"sync"
	"testing"
)

// The contract values of result and reason. The tests do not use the production constants, so
// a change to a production wire value makes a test fail.
const (
	wantOK             = "ok"
	wantDenied         = "denied"
	wantError          = "error"
	wantRoute          = "route"
	wantSpanTrace      = "trace"
	wantMethod         = "method"
	wantAuthentication = "authentication"
	wantAuthorization  = "authorization"
	wantReview         = "review"
	wantEntropy        = "entropy"
	wantTarget         = "target"
	wantUpstream       = "upstream"
	wantStream         = "stream"
	wantCancelled      = "cancelled"
)

// spanCanary is a random value that no test prints.
func spanCanary() string { return rand.Text() }

func TestSpanProxyStatus(t *testing.T) {
	for o, want := range map[spanOutcome]int{
		outcomeRoute: 400, outcomeTrace: 400, outcomeMethod: 405, outcomeAuthentication: 401,
		outcomeAuthorization: 403, outcomeReview: 503, outcomeEntropy: 503, outcomeTargetAbsent: 404,
		outcomeTargetUnavailable: 503, outcomeUpstream: 502, outcomeOK: 0, outcomeStream: 0, outcomeCancelled: 0,
	} {
		if proxyStatus(o) != want {
			t.Fatal("a proxy status is not the contract value")
		}
	}
}

func TestSpanMethodSet(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if spanMethod(m) != m {
			t.Fatal("an approved method was not kept")
		}
	}
	secret := spanCanary()
	for _, m := range []string{"CONNECT", "get", "TRACE", "", secret} {
		if spanMethod(m) != "OTHER" {
			t.Fatal("a method outside the set was not OTHER")
		}
	}
}

func TestSpanCounter(t *testing.T) {
	var c spanCounter
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r := countingReader{r: bytes.NewReader(make([]byte, 1000)), c: &c}
			buf := make([]byte, 7)
			for {
				if _, err := r.Read(buf); err != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	if c.load() != 8000 {
		t.Fatal("the counter lost bytes")
	}
	c.add(-5)
	c.add(0)
	if c.load() != 8000 {
		t.Fatal("the counter took a nonpositive count")
	}
	c.n.Store(math.MaxInt64 - 1)
	c.add(10)
	if c.load() != math.MaxInt64 {
		t.Fatal("the counter did not stop at the maximum")
	}
}
