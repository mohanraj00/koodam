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

package conversion

import (
	"net/http"
	"sync"
	"time"
)

// ReasonBusy is the reason of a refusal when the requests in progress use all the body budget.
const ReasonBusy = "too many conversion requests are in progress"

// minReservation is the part of the body budget that a small request reserves. It makes the cap
// on the number of requests in progress: budget / minReservation.
const minReservation = 1 << 20

// limits are the limits of the server. The API server gives no client certificate to a
// conversion webhook in the default configuration, so each client that can connect can send a
// request. The limits keep the time and the memory of each client small.
type limits struct {
	// readHeader is the time to read the header of a request. It is also the time for the TLS
	// handshake.
	readHeader time.Duration
	// read is the time to read a full request, with its body.
	//
	// The API server has no time limit of its own for a conversion call: the call ends with the
	// API request, and the default --request-timeout is 60 seconds. A body that needs more time
	// is of no use to an API server with that default. Thus the limit is 60 seconds. A body of
	// MaxRequestBytes (64 MiB, which is 537 megabits) then arrives in time on a link that moves
	// 9 megabits or more in each second.
	read time.Duration
	// write is the time from the end of the header to the end of the response. It includes the
	// read of the body, so it is the read time and 30 seconds for the conversion and the response.
	write time.Duration
	// idle is the time that a connection with no request stays open. It is the keep-alive time of
	// the Go HTTP client, which the API server uses.
	idle time.Duration
	// shutdown is the time for the requests in progress when the server stops. The pod gets 10
	// seconds to stop.
	shutdown time.Duration
	// headerBytes is the limit of a request header.
	headerBytes int
	// bodyBudget is the limit of the request bodies of all the requests in progress.
	//
	// MaxRequestBytes stays the limit of one body, because the API server sends all the objects
	// of a list in one request, and a refusal of that request makes the list fail. But the
	// handler holds the body, the objects and the response in memory at the same time, which is
	// some times the size of the body. Thus the budget is one body of the largest size: one
	// request of that size is always accepted when no other request is in progress, and two are
	// not in progress at the same time. Small requests reserve minReservation, so 64 of them can
	// be in progress.
	bodyBudget int64
}

var defaultLimits = limits{
	readHeader:  10 * time.Second,
	read:        60 * time.Second,
	write:       90 * time.Second,
	idle:        90 * time.Second,
	shutdown:    8 * time.Second,
	headerBytes: 64 << 10,
	bodyBudget:  MaxRequestBytes,
}

// limiter is a cap on the requests in progress of one handler. Each request reserves the size of
// its body from the budget. A request that the budget cannot hold gets the code 429 with the
// header Retry-After.
//
// The API server calls a conversion webhook with the client-go REST client and gives it the
// review as an object (apiextensions-apiserver, pkg/apiserver/conversion/webhook_converter.go).
// That client sends a request with such a body again, a maximum of 10 times, when the response
// is 429 or 5xx and has Retry-After (client-go, rest/with_retry.go). It does not send the request
// again for a 429 with no Retry-After, so the header is necessary.
type limiter struct {
	next    http.Handler
	refused func(reason string)
	budget  int64

	mu   sync.Mutex
	used int64
}

func newLimiter(next http.Handler, refused func(reason string), budget int64) *limiter {
	return &limiter{next: next, refused: refused, budget: budget}
}

// reservation returns the part of the budget for a request with a body of length bytes. A
// length that is not known, or is more than the limit of one body, reserves that limit.
func reservation(length int64) int64 {
	if length < 0 || length > MaxRequestBytes {
		return MaxRequestBytes
	}
	return max(length, minReservation)
}

func (l *limiter) reserve(n int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used+n > l.budget {
		return false
	}
	l.used += n
	return true
}

func (l *limiter) release(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.used -= n
}

func (l *limiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := reservation(r.ContentLength)
	if !l.reserve(n) {
		l.refused(ReasonBusy)
		w.Header().Set("Retry-After", "1")
		http.Error(w, ReasonBusy, http.StatusTooManyRequests)
		return
	}
	// The release is deferred, so a panic of the handler does not keep the reservation.
	defer l.release(n)
	l.next.ServeHTTP(w, r)
}
