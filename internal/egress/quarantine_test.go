/*
Copyright 2026.

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

package egress

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const (
	qAgent   = "q-agent"
	qHold    = "q-hold"
	qAdmin   = "admin@example.test"
	qRelease = "admin-b@example.test"
	qProfile = "q-profile"
	otherNS  = "other"
)

// hold returns a Hold with the target kind, namespace and name. A release user releases it.
func hold(name, action, kind, namespace, target, release string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion("core.koodam.dev/v1beta1")
	u.SetKind("Hold")
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, action, "spec", "action")
	_ = unstructured.SetNestedField(u.Object, kind, "spec", "target", "kind")
	if namespace != "" {
		_ = unstructured.SetNestedField(u.Object, namespace, "spec", "target", "namespace")
	}
	_ = unstructured.SetNestedField(u.Object, target, "spec", "target", "name")
	_ = unstructured.SetNestedField(u.Object, qAdmin, "spec", "user")
	_ = unstructured.SetNestedField(u.Object, "incident 7", "spec", "reason")
	if release != "" {
		_ = unstructured.SetNestedField(u.Object, map[string]any{"user": release, "reason": "clean"}, "spec", "release")
	}
	return u
}

func profile(name string, bound ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion("core.koodam.dev/v1beta1")
	u.SetKind("Profile")
	u.SetName(name)
	if bound != nil {
		_ = unstructured.SetNestedStringSlice(u.Object, bound, "status", "boundNamespaces")
	}
	return u
}

func TestQuarantineMatch(t *testing.T) {
	sp := &Span{Namespace: ns, Agent: qAgent, Workspace: "ws", App: appName, AppVersion: appV1}
	bound := map[string][]string{qProfile: {otherNS, ns}, "empty": nil}
	for _, c := range []struct {
		q    quarantine
		want bool
	}{
		{quarantine{kind: targetAgent, namespace: ns, name: qAgent}, true},
		{quarantine{kind: targetAgent, namespace: otherNS, name: qAgent}, false},
		{quarantine{kind: targetAgent, namespace: ns, name: "ws"}, false},
		{quarantine{kind: targetWS, namespace: ns, name: "ws"}, true},
		{quarantine{kind: targetApp, namespace: ns, name: appName}, true},
		{quarantine{kind: targetVersion, namespace: ns, name: appV1}, true},
		{quarantine{kind: targetVersion, namespace: ns, name: appName}, false},
		{quarantine{kind: targetProfile, name: qProfile}, true},
		{quarantine{kind: targetProfile, name: "empty"}, false},
		{quarantine{kind: targetProfile, name: "unknown"}, false},
		// A target with no namespace or an unknown kind matches no pod.
		{quarantine{kind: targetAgent, name: qAgent}, false},
		{quarantine{kind: "Other", namespace: ns, name: qAgent}, false},
	} {
		if got := c.q.match(sp, bound); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.q, got, c.want)
		}
	}
	// A pod with no identity matches no quarantine, also an empty name.
	if (quarantine{kind: targetAgent, namespace: ns}).match(&Span{}, bound) ||
		(quarantine{kind: targetProfile, name: qProfile}).match(&Span{Agent: qAgent}, map[string][]string{qProfile: {""}}) {
		t.Fatal("a pod with no namespace matches a quarantine")
	}
}

// A new quarantine ends the open calls of its pods and refuses each new call. A release opens the
// pods again. Each start and each release writes 1 control span.
func TestQuarantinesAdmitAndCut(t *testing.T) {
	sink := make(chanSink, 4)
	q := NewQuarantines()
	q.Sink = sink
	now := time.Date(2026, 10, 6, 8, 5, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }
	agent := &Span{Namespace: ns, Agent: qAgent}
	otherPod := &Span{Namespace: ns, Agent: qAgent + "-b"}

	var stopped, otherStopped atomic.Int32
	done, ok := q.Admit(agent, func() { stopped.Add(1) })
	otherDone, otherOK := q.Admit(otherPod, func() { otherStopped.Add(1) })
	if !ok || !otherOK {
		t.Fatal("a call with no quarantine is refused")
	}
	q.setHold(hold(qHold, "Stop", targetAgent, ns, qAgent, ""))
	if stopped.Load() != 0 || len(sink) != 0 {
		t.Fatal("a Stop Hold changed the proxy")
	}
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	if stopped.Load() != 1 || otherStopped.Load() != 0 {
		t.Fatalf("the stops are %d and %d, want 1 and 0", stopped.Load(), otherStopped.Load())
	}
	if !done() || otherDone() {
		t.Fatal("done does not report the cut call")
	}
	sp := sink.next(t)
	if sp.V != SpanVersion || sp.Kind != KindControl || sp.Result != ResultQuarantined || sp.By != qAdmin ||
		sp.Reason != "incident 7" || sp.Namespace != ns || sp.Agent != qAgent || !sp.Time.Equal(now) || sp.User != "" {
		t.Fatalf("control span %+v", sp)
	}
	if _, ok := q.Admit(agent, func() {}); ok {
		t.Fatal("a call of a quarantined pod is admitted")
	}
	if _, ok := q.Admit(otherPod, func() {}); !ok {
		t.Fatal("a call of a different pod is refused")
	}
	// A resync of the same Hold writes no span and stops nothing again.
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	if len(sink) != 0 || stopped.Load() != 1 {
		t.Fatal("a resync wrote a span or stopped a call")
	}

	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, qRelease))
	sp = sink.next(t)
	if sp.Kind != KindControl || sp.Result != ResultReleased || sp.By != qRelease || sp.Reason != "clean" ||
		sp.Agent != qAgent {
		t.Fatalf("release span %+v", sp)
	}
	if _, ok := q.Admit(agent, func() {}); !ok {
		t.Fatal("a released pod is refused")
	}
	// A second release event and the delete write nothing.
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, qRelease))
	q.end(qHold, "", "")
	if len(sink) != 0 {
		t.Fatal("a known release wrote a second span")
	}
}

// A Profile quarantine matches each pod in a bound namespace of the Profile. A new bound
// namespace ends the open calls in it.
func TestQuarantinesProfile(t *testing.T) {
	sink := make(chanSink, 4)
	q := NewQuarantines()
	q.Sink = sink
	q.setProfile(profile(qProfile, ns))
	q.setHold(hold(qHold, holdQuarantine, targetProfile, "", qProfile, ""))
	if sp := sink.next(t); sp.Kind != KindControl || sp.Namespace != ns || sp.Result != ResultQuarantined {
		t.Fatalf("control span %+v", sp)
	}
	if _, ok := q.Admit(&Span{Namespace: ns, Pod: "any"}, func() {}); ok {
		t.Fatal("a pod in a bound namespace is admitted")
	}
	var stopped atomic.Int32
	done, ok := q.Admit(&Span{Namespace: "later", Pod: "p"}, func() { stopped.Add(1) })
	if !ok {
		t.Fatal("a pod in a namespace that is not bound is refused")
	}
	q.setProfile(profile(qProfile, ns, "later"))
	if stopped.Load() != 1 || !done() {
		t.Fatal("a new bound namespace did not end the open call")
	}
	// A new bound namespace gets its start span. A resync of the Profile writes no span.
	if sp := sink.next(t); sp.Namespace != "later" || sp.Result != ResultQuarantined || sp.By != qAdmin {
		t.Fatalf("control span %+v", sp)
	}
	q.setProfile(profile(qProfile, ns, "later"))
	if len(sink) != 0 {
		t.Fatal("a resync of the Profile wrote a span")
	}
	q.removeProfile(qProfile)
	if _, ok := q.Admit(&Span{Namespace: ns}, func() {}); !ok {
		t.Fatal("a pod of a deleted Profile is refused")
	}
}

type quarantineSinkFunc func(Span)

func (f quarantineSinkFunc) Write(sp Span) { f(sp) }

func TestQuarantinesProfileReleaseSpanOrder(t *testing.T) {
	startHold := hold(qHold, holdQuarantine, targetProfile, "", qProfile, "")
	releaseHold := hold(qHold, holdQuarantine, targetProfile, "", qProfile, qRelease)
	initialProfile := profile(qProfile, ns)
	updatedProfile := profile(qProfile, ns, otherNS)
	for i := range 200 {
		var spans []Span
		var spansMu sync.Mutex
		q := NewQuarantines()
		q.Sink = quarantineSinkFunc(func(sp Span) {
			// The sink delays the start span of the new namespace.
			if sp.Result == ResultQuarantined && sp.Namespace == otherNS {
				time.Sleep(time.Millisecond)
			}
			spansMu.Lock()
			spans = append(spans, sp)
			spansMu.Unlock()
		})
		q.setProfile(initialProfile)
		q.setHold(startHold)

		start := make(chan struct{})
		var changes sync.WaitGroup
		changes.Go(func() {
			<-start
			q.setProfile(updatedProfile)
		})
		changes.Go(func() {
			<-start
			q.setHold(releaseHold)
		})
		close(start)
		changes.Wait()

		var starts, releases int
		for _, sp := range spans {
			switch sp.Result {
			case ResultQuarantined:
				if releases > 0 {
					t.Fatalf("iteration %d: quarantined span for %s after a released span", i, sp.Namespace)
				}
				starts++
			case ResultReleased:
				releases++
			}
		}
		if starts == 0 || starts != releases {
			t.Fatalf("iteration %d: %d quarantined spans and %d released spans", i, starts, releases)
		}
	}
}

func TestQuarantinesAdmitDuringControlWrite(t *testing.T) {
	q := NewQuarantines()
	writing := make(chan struct{})
	resume := make(chan struct{})
	q.Sink = quarantineSinkFunc(func(Span) {
		close(writing)
		<-resume
	})
	var pending sync.WaitGroup
	defer func() {
		close(resume)
		pending.Wait()
	}()
	pending.Go(func() {
		q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	})
	select {
	case <-writing:
	case <-time.After(time.Second):
		t.Fatal("the sink did not get a control span")
	}
	admitted := make(chan bool, 1)
	pending.Go(func() {
		_, ok := q.Admit(&Span{Namespace: ns, Agent: qAgent}, func() {})
		admitted <- ok
	})
	select {
	case ok := <-admitted:
		if ok {
			t.Fatal("a call of a quarantined pod is admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("Admit waits for the sink")
	}
}

// If the index reads the Hold before its Profile, the Profile gives the start span of each bound
// namespace. The user and the reason come from the Hold.
func TestQuarantinesHoldBeforeProfile(t *testing.T) {
	sink := make(chanSink, 4)
	q := NewQuarantines()
	q.Sink = sink
	q.setHold(hold(qHold, holdQuarantine, targetProfile, "", qProfile, ""))
	if len(sink) != 0 {
		t.Fatal("a Profile quarantine with no known namespace wrote a span")
	}
	q.setProfile(profile(otherNS))
	q.setProfile(profile(qProfile, ns, otherNS))
	got := map[string]bool{}
	for range 2 {
		sp := sink.next(t)
		if sp.Kind != KindControl || sp.Result != ResultQuarantined || sp.By != qAdmin || sp.Reason != "incident 7" {
			t.Fatalf("control span %+v", sp)
		}
		got[sp.Namespace] = true
	}
	if !got[ns] || !got[otherNS] || len(sink) != 0 {
		t.Fatalf("the start spans are in %v, want %s and %s", got, ns, otherNS)
	}
	if _, ok := q.Admit(&Span{Namespace: ns}, func() {}); ok {
		t.Fatal("a pod in a bound namespace is admitted")
	}
	q.setHold(hold(qHold, holdQuarantine, targetProfile, "", qProfile, qRelease))
	for range 2 {
		if sp := sink.next(t); sp.Result != ResultReleased || sp.By != qRelease {
			t.Fatalf("release span %+v", sp)
		}
	}
}

// Watch reads the Holds and the Profiles from the API server.
func TestQuarantinesWatch(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{Holds: "HoldList", Profiles: "ProfileList"},
		hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""), profile(qProfile, "bound"))
	q := NewQuarantines()
	ctx := t.Context()
	if err := q.Watch(ctx, client, 0); err != nil {
		t.Fatal(err)
	}
	// The caches are in sync when Watch returns.
	if _, ok := q.Admit(&Span{Namespace: ns, Agent: qAgent}, func() {}); ok {
		t.Fatal("the Hold before the watch is not active")
	}
	holds := client.Resource(Holds)
	if _, err := holds.Create(ctx, hold("q-profile-hold", holdQuarantine, targetProfile, "", qProfile, ""),
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Profile quarantine", func() bool { _, ok := q.Admit(&Span{Namespace: "bound"}, func() {}); return !ok })
	if _, err := holds.Update(ctx, hold(qHold, holdQuarantine, targetAgent, ns, qAgent, qRelease),
		metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the release", func() bool { _, ok := q.Admit(&Span{Namespace: ns, Agent: qAgent}, func() {}); return ok })
	if err := holds.Delete(ctx, "q-profile-hold", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the delete", func() bool { _, ok := q.Admit(&Span{Namespace: "bound"}, func() {}); return ok })
}

// withQuarantine gives the proxy pods with the identity of qAgent and the quarantine of q.
func withQuarantine(q *Quarantines) func(*Proxy) {
	return func(p *Proxy) {
		p.Identify = func(_ *http.Request, sp *Span) error {
			sp.Namespace, sp.Agent = ns, qAgent
			return nil
		}
		p.Quarantine = q.Admit
	}
}

func TestProxyRefusesAQuarantinedPod(t *testing.T) {
	q := NewQuarantines()
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	sink := make(chanSink, 1)
	p := &Proxy{Sink: sink}
	withQuarantine(q)(p)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil))
	sp := sink.next(t)
	if w.Code != http.StatusForbidden || sp.Result != ResultDenied || sp.Reason != ReasonQuarantined ||
		sp.Agent != qAgent {
		t.Fatalf("status %d, span %+v", w.Code, sp)
	}
	if len(q.calls) != 0 {
		t.Fatal("a refused call stays in the index")
	}
}

// A quarantine ends an open tunnel. Its span is denied with the reason quarantined.
func TestConnectQuarantineEndsTheTunnel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			if _, err := c.Read(make([]byte, 1)); err == nil {
				close(got)
			}
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	lnPort := port(t, "http://"+ln.Addr().String())
	q := NewQuarantines()
	pu, sink := start(t, false, lnPort, nil, withQuarantine(q))
	c, _ := connect(t, pu, net.JoinHostPort(testHost, lnPort))
	defer func() { _ = c.Close() }()
	tc := tls.Client(c, &tls.Config{ServerName: testHost})
	go func() { _ = tc.Handshake() }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream got no ClientHello")
	}
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	if sp := sink.next(t); sp.Kind != KindConnect || sp.Result != ResultDenied || sp.Reason != ReasonQuarantined {
		t.Fatalf("span %+v", sp)
	}
}

// A quarantine ends an open transparent TLS connection, and refuses the next one.
func TestTransparentTLSQuarantine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			if _, err := c.Read(make([]byte, 1)); err == nil {
				close(got)
			}
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	q := NewQuarantines()
	addr, sink := startTLS(t, false, port(t, "http://"+ln.Addr().String()), withQuarantine(q))
	dial := func() net.Conn {
		c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = tls.Client(c, &tls.Config{ServerName: testHost}).Handshake() }()
		return c
	}
	c := dial()
	defer func() { _ = c.Close() }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream got no ClientHello")
	}
	q.setHold(hold(qHold, holdQuarantine, targetAgent, ns, qAgent, ""))
	if sp := sink.next(t); sp.Kind != KindTLS || sp.Result != ResultDenied || sp.Reason != ReasonQuarantined {
		t.Fatalf("span %+v", sp)
	}
	c2 := dial()
	defer func() { _ = c2.Close() }()
	if sp := sink.next(t); sp.Kind != KindTLS || sp.Result != ResultDenied || sp.Reason != ReasonQuarantined {
		t.Fatalf("span %+v", sp)
	}
}
