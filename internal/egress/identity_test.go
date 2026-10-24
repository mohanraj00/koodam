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
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/mohanraj00/koodam/internal/controller"
)

const (
	podA  = "10.0.0.5"
	podB  = "10.0.0.6"
	ns    = "team"
	alice = "alice"
	bob   = "bob"
	run1  = "a1-run"
	pod1  = "a1-run-x"
)

func pod(name, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}},
	}
}

func agentPod(name, ip string) *corev1.Pod {
	return pod(name, ip, map[string]string{AgentLabel: "a1", RunLabel: run1, UserLabel: alice})
}

func identify(t *testing.T, x *PodIndex, source string, header http.Header) (Span, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil)
	maps.Copy(r.Header, header)
	sp := Span{Source: source}
	err := x.Identify(r, &sp)
	if r.Header.Get(ClaimedRunHeader) != "" || r.Header.Get(ClaimedUserHeader) != "" {
		t.Fatal("a claim header goes upstream")
	}
	return sp, err
}

func TestLabelsAgreeWithTheController(t *testing.T) {
	if RunLabel != controller.RunLabel || UserLabel != controller.UserLabel ||
		WorkspaceLabel != controller.WorkspaceLabel {
		t.Fatal("the egress labels are not the controller labels")
	}
}

func TestIdentifyKnownPod(t *testing.T) {
	x := NewPodIndex()
	x.Set(agentPod(pod1, podA))
	x.Set(pod("ws-0", podB, map[string]string{WorkspaceLabel: "ws", UserLabel: bob}))

	sp, err := identify(t, x, podA, nil)
	if err != nil || sp.Namespace != ns || sp.Pod != pod1 || sp.Agent != "a1" || sp.Run != run1 ||
		sp.User != alice || sp.Workspace != "" {
		t.Fatalf("%v %+v", err, sp)
	}
	sp, err = identify(t, x, podB, nil)
	if err != nil || sp.Workspace != "ws" || sp.User != bob || sp.Agent != "" {
		t.Fatalf("%v %+v", err, sp)
	}
}

func TestIdentifyRecordsClaimsOnly(t *testing.T) {
	x := NewPodIndex()
	x.Set(agentPod(pod1, podA))
	h := http.Header{}
	h.Set(ClaimedRunHeader, "other-run")
	h.Set(ClaimedUserHeader, "mallory")
	sp, err := identify(t, x, podA, h)
	if err != nil || sp.Run != run1 || sp.User != alice || !sp.Claimed {
		t.Fatalf("%v %+v", err, sp)
	}
	// The span holds no claim value, because the value can be a key (#170).
	if b, _ := json.Marshal(sp); strings.Contains(string(b), "other-run") || strings.Contains(string(b), "mallory") {
		t.Fatalf("the span holds a claim: %s", b)
	}
	if sp, _ = identify(t, x, podA, nil); sp.Claimed {
		t.Fatalf("no claim: %+v", sp)
	}
	// A claim does not give an identity to an unknown IP.
	sp, err = identify(t, x, podB, h)
	if err == nil || sp.User != Unknown || sp.Run != "" {
		t.Fatalf("%v %+v", err, sp)
	}
}

func TestIndexRefuses(t *testing.T) {
	ended := agentPod("ended", podA)
	ended.Status.Phase = corev1.PodSucceeded
	deleting := agentPod("deleting", podA)
	deleting.DeletionTimestamp = new(metav1.Now())
	for name, pods := range map[string][]*corev1.Pod{
		"unknown IP":   nil,
		"shared IP":    {agentPod("one", podA), agentPod("two", podA)},
		"ended pod":    {ended},
		"deleting pod": {deleting},
		"no label":     {pod("other", podA, map[string]string{UserLabel: alice})},
	} {
		t.Run(name, func(t *testing.T) {
			x := NewPodIndex()
			for _, p := range pods {
				x.Set(p)
			}
			sp, err := identify(t, x, podA, nil)
			if err == nil || sp.Pod != Unknown || sp.User != Unknown || sp.Run != "" {
				t.Fatalf("%v %+v", err, sp)
			}
		})
	}
}

func TestIndexFollowsTheIP(t *testing.T) {
	x := NewPodIndex()
	p := agentPod(pod1, podA)
	x.Set(p)
	// The pod gets a new IP. The old IP has no pod.
	p = agentPod(pod1, podB)
	x.Set(p)
	if _, err := identify(t, x, podA, nil); err == nil {
		t.Fatal("the old IP has an identity")
	}
	if sp, err := identify(t, x, podB, nil); err != nil || sp.Pod != pod1 {
		t.Fatalf("%v %+v", err, sp)
	}
	// A deleted pod gives its IP to a new pod of a different run.
	x.Delete(ns, pod1)
	x.Set(pod("b1-run-y", podB, map[string]string{AgentLabel: "b1", RunLabel: "b1-run", UserLabel: bob}))
	if sp, err := identify(t, x, podB, nil); err != nil || sp.Run != "b1-run" || sp.User != bob {
		t.Fatalf("%v %+v", err, sp)
	}
}

func TestProxyRefusesUnknownSource(t *testing.T) {
	sink := make(chanSink, 1)
	p := &Proxy{Sink: sink, Identify: NewPodIndex().Identify}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil)
	r.RemoteAddr = podA + ":4000"
	p.ServeHTTP(w, r)
	sp := sink.next(t)
	if w.Code != http.StatusForbidden || sp.Reason != reasonIdentity || sp.Pod != Unknown {
		t.Fatalf("status %d, span %+v", w.Code, sp)
	}
}

func TestProxyIdentifiesKnownSource(t *testing.T) {
	x := NewPodIndex()
	x.Set(agentPod(pod1, podA))
	sink := make(chanSink, 1)
	p := &Proxy{Sink: sink, Identify: x.Identify}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = podA + ":4000"
	p.ServeHTTP(httptest.NewRecorder(), r)
	// The request is not a proxy request, but the span has the identity of the pod.
	if sp := sink.next(t); sp.Reason != reasonNoProxy || sp.Pod != pod1 || sp.Run != run1 {
		t.Fatalf("span %+v", sp)
	}
}

func TestIndexIPForms(t *testing.T) {
	x := NewPodIndex()
	p := agentPod("v6", "")
	p.Status.PodIPs = []corev1.PodIP{{IP: ""}, {IP: "FD00:0:0::5"}}
	x.Set(p)
	if sp, err := identify(t, x, "fd00::5", nil); err != nil || sp.Pod != "v6" {
		t.Fatalf("%v %+v", err, sp)
	}
	if _, err := identify(t, x, "", nil); err == nil {
		t.Fatal("an empty source has an identity")
	}
}

func TestIndexTombstone(t *testing.T) {
	x := NewPodIndex()
	x.Set(agentPod(pod1, podA))
	x.handler().OnDelete(cache.DeletedFinalStateUnknown{Key: ns + "/" + pod1})
	if _, err := identify(t, x, podA, nil); err == nil {
		t.Fatal("the pod stays after a tombstone")
	}
}

func TestWatch(t *testing.T) {
	cs := fake.NewClientset(agentPod(pod1, podA),
		pod("ws-0", podB, map[string]string{WorkspaceLabel: "ws", UserLabel: bob}))
	ctx := t.Context()
	x := NewPodIndex()
	if err := x.Watch(ctx, cs, 0); err != nil {
		t.Fatal(err)
	}
	if sp, err := identify(t, x, podA, nil); err != nil || sp.Run != run1 {
		t.Fatalf("%v %+v", err, sp)
	}
	if sp, err := identify(t, x, podB, nil); err != nil || sp.Workspace != "ws" {
		t.Fatalf("%v %+v", err, sp)
	}
	if err := cs.CoreV1().Pods(ns).Delete(ctx, pod1, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := identify(t, x, podA, nil); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted pod stays in the index")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWatchNamespaces(t *testing.T) {
	cs := fake.NewClientset(agentPod(pod1, podA),
		pod("ws-0", podB, map[string]string{WorkspaceLabel: "ws", UserLabel: bob}))
	other := NewPodIndex()
	if err := other.Watch(t.Context(), cs, 0, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := identify(t, other, podA, nil); err == nil {
		t.Fatal("the index has a pod from a namespace that it does not watch")
	}
	x := NewPodIndex()
	if err := x.Watch(t.Context(), cs, 0, ns, "other", ns); err != nil {
		t.Fatal(err)
	}
	if sp, err := identify(t, x, podA, nil); err != nil || sp.Run != run1 {
		t.Fatalf("%v %+v", err, sp)
	}
	if sp, err := identify(t, x, podB, nil); err != nil || sp.Workspace != "ws" {
		t.Fatalf("%v %+v", err, sp)
	}
}
