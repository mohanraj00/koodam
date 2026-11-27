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

package spendapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const watchPath = testPath + "/" + watchPart

// latencyTarget is the target of #399: a phase change reaches a watch client in less than 5 s.
const latencyTarget = 5 * time.Second

type sseEvent struct{ name, data string }

// watchStream connects to the watch path of a real server. The server has short read and write
// time limits, so the tests also show that a watch continues after them.
type watchStream struct {
	events chan sseEvent
	resp   *http.Response
}

func newWatchFixture(t *testing.T) (*viewerFixture, client.WithWatch, *httptest.Server) {
	t.Helper()
	f := newViewerFixture(t)
	kube := runClient(t, runObjects(f.runKeys[0].hash)...)
	f.kube, f.watcher = kube, kube
	server := httptest.NewUnstartedServer(f.api)
	server.Config.ReadTimeout = 200 * time.Millisecond
	server.Config.WriteTimeout = 500 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)
	return f, kube, server
}

func openWatch(t *testing.T, server *httptest.Server, token string) *watchStream {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+watchPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("the watch gives %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := &watchStream{events: make(chan sseEvent, 16), resp: resp}
	go func() {
		defer close(s.events)
		var e sseEvent
		lines := bufio.NewScanner(resp.Body)
		for lines.Scan() {
			name, value, _ := strings.Cut(lines.Text(), ": ")
			switch name {
			case "event":
				e.name = value
			case "data":
				e.data = value
			case "":
				if e.name != "" {
					s.events <- e
				}
				e = sseEvent{}
			}
		}
	}()
	return s
}

func (s *watchStream) next(t *testing.T) (string, runStatus) {
	t.Helper()
	select {
	case e, ok := <-s.events:
		if !ok {
			t.Fatal("the watch ended with no event")
		}
		var status runStatus
		if err := json.Unmarshal([]byte(e.data), &status); err != nil {
			t.Fatalf("the data of the event %s is not JSON: %v", e.name, err)
		}
		return e.name, status
	case <-time.After(2 * latencyTarget):
		t.Fatal("no event arrived")
	}
	return "", runStatus{}
}

func setPhase(t *testing.T, kube client.Client, phase corev1alpha1.AgentPhase) {
	t.Helper()
	agent := &corev1alpha1.Agent{}
	if err := kube.Get(t.Context(), client.ObjectKey{Namespace: testNamespace, Name: runName}, agent); err != nil {
		t.Fatal(err)
	}
	agent.Status.Phase = phase
	if err := kube.Update(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
}

// TestWatchLatency is the latency test of #399. The change comes after the server read time limit,
// and it reaches the client in less than the target.
func TestWatchLatency(t *testing.T) {
	f, kube, server := newWatchFixture(t)
	s := openWatch(t, server, f.reader)
	if name, status := s.next(t); name != eventStatus || status.Phase != string(corev1alpha1.AgentRunning) ||
		status.Name != runName || len(status.Conditions) != 1 {
		t.Fatalf("the first event is %s %+v", name, status)
	}
	time.Sleep(700 * time.Millisecond)
	start := time.Now()
	setPhase(t, kube, corev1alpha1.AgentSucceeded)
	name, status := s.next(t)
	if latency := time.Since(start); latency >= latencyTarget {
		t.Fatalf("the phase change took %s, want less than %s", latency, latencyTarget)
	}
	if name != eventStatus || status.Phase != string(corev1alpha1.AgentSucceeded) {
		t.Fatalf("the event is %s %+v", name, status)
	}
	agent := &corev1alpha1.Agent{}
	agent.Namespace, agent.Name = testNamespace, runName
	if err := kube.Delete(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.next(t); name != "deleted" {
		t.Fatalf("want a deleted event, got %s", name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, review := range f.reviews {
		a := review.ResourceAttributes
		if a.Namespace != testNamespace || a.Verb != listAgents.Verb || a.Resource != listAgents.Resource {
			t.Fatalf("want the review of list agents in the run namespace only: %+v", a)
		}
	}
}

func TestWatchTimeLimit(t *testing.T) {
	f, _, server := newWatchFixture(t)
	f.watchTime = 400 * time.Millisecond
	s := openWatch(t, server, f.reader)
	if name, _ := s.next(t); name != eventStatus {
		t.Fatalf("the first event is %s", name)
	}
	select {
	case e := <-s.events:
		if e.name != "end" || e.data != `{"reason":"timeLimit"}` {
			t.Fatalf("want an end event for the time limit, got %+v", e)
		}
	case <-time.After(latencyTarget):
		t.Fatal("the watch did not stop at its time limit")
	}
	if f.watches.Load() != 0 {
		t.Fatal("the count of open watches is not 0 after the watch")
	}
}

func TestWatchRefusals(t *testing.T) {
	f, _, _ := newWatchFixture(t)
	f.watches.Store(maxOpenWatches)
	w := f.call(t, http.MethodGet, watchPath, f.reader, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" || f.watches.Load() != maxOpenWatches {
		t.Fatal("a full server does not refuse the watch correctly")
	}
	f.watches.Store(0)
	if w := f.call(t, http.MethodHead, watchPath, f.reader, http.StatusOK); w.Body.Len() != 0 ||
		w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("HEAD of the watch is not an empty event stream")
	}
	f.call(t, http.MethodGet, watchPath+"?x=1", f.reader, http.StatusBadRequest)
	f.call(t, http.MethodGet, runPrefix+testNamespace+"/missing/watch", f.reader, http.StatusNotFound)
	f.call(t, http.MethodGet, runPrefix+otherNamespace+"/"+runName+"/watch", f.reader, http.StatusForbidden)
	f.call(t, http.MethodGet, watchPath, f.stranger, http.StatusForbidden)
	f.call(t, http.MethodGet, watchPath, "", http.StatusUnauthorized)
	f.kube = failingGet{err: errors.New("the API server is not available")}
	f.call(t, http.MethodGet, watchPath, f.reader, http.StatusBadGateway)
	f.watcher = nil
	f.call(t, http.MethodGet, watchPath, f.reader, http.StatusNotFound)
	if f.watches.Load() != 0 {
		t.Fatal("the count of open watches is not 0 after the refusals")
	}
}
