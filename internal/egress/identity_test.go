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
	"errors"
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
	x.handler(nil, true).OnDelete(cache.DeletedFinalStateUnknown{Key: ns + "/" + pod1})
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

const (
	podC    = "10.0.0.7"
	prod    = "prod"
	shop0   = "shop-0"
	appName = "shop"
	appV1   = "shop-v1"
	appV2   = "shop-v2"
)

func appPod(name, ip, version string) *corev1.Pod {
	return pod(name, ip, map[string]string{AppLabel: appName, AppVersionLabel: version})
}

func TestIdentifyAppPod(t *testing.T) {
	x := NewPodIndex()
	p := appPod("shop-v2-0", podA, appV2)
	// The user is the user label, as for each pod.
	p.Labels[UserLabel] = alice
	x.Set(p)
	sp, err := identify(t, x, podA, nil)
	if err != nil || sp.Namespace != ns || sp.Pod != "shop-v2-0" || sp.App != appName || sp.AppVersion != appV2 ||
		sp.Agent != "" || sp.Workspace != "" || sp.Run != "" || sp.User != alice {
		t.Fatalf("%v %+v", err, sp)
	}
	b, _ := json.Marshal(sp)
	if !strings.Contains(string(b), `"user":"alice","app":"shop","appVersion":"shop-v2"`) {
		t.Fatalf("the span is %s", b)
	}
	// An App pod with no user label has no user.
	x.Set(appPod("shop-v2-0", podA, appV2))
	if sp, err = identify(t, x, podA, nil); err != nil || sp.User != "" {
		t.Fatalf("%v %+v", err, sp)
	}
	if b, _ = json.Marshal(sp); strings.Contains(string(b), `"user"`) {
		t.Fatalf("the span is %s", b)
	}
}

func TestSpanOmitsAppForOldIdentities(t *testing.T) {
	x := NewPodIndex()
	x.Set(agentPod(pod1, podA))
	x.Set(pod("ws-0", podB, map[string]string{WorkspaceLabel: "ws", UserLabel: bob}))
	for _, source := range []string{podA, podB} {
		sp, err := identify(t, x, source, nil)
		b, _ := json.Marshal(sp)
		if err != nil || strings.Contains(string(b), `"app`) {
			t.Fatalf("%v %s", err, b)
		}
	}
	sp := Span{V: SpanVersion, Source: podA, Kind: KindHTTP, Result: ResultOK, Namespace: ns, Pod: pod1,
		Agent: "a1", Run: run1, User: alice}
	b, _ := json.Marshal(sp)
	want := `{"v":2,"time":"0001-01-01T00:00:00Z","durationMs":0,"source":"10.0.0.5","kind":"http",` +
		`"bytesIn":0,"bytesOut":0,"result":"ok","namespace":"team","pod":"a1-run-x","agent":"a1",` +
		`"run":"a1-run","user":"alice"}`
	if string(b) != want {
		t.Fatalf("the span is\n%s\nwant\n%s", b, want)
	}
}

func TestIndexRefusesAppLabels(t *testing.T) {
	with := func(extra map[string]string) *corev1.Pod {
		p := appPod(shop0, podA, appV1)
		maps.Copy(p.Labels, extra)
		return p
	}
	agentVersion := agentPod("a", podA)
	agentVersion.Labels[AppVersionLabel] = appV1
	for name, pods := range map[string][]*corev1.Pod{
		"app only":       {pod("p", podA, map[string]string{AppLabel: appName})},
		"version only":   {pod("p", podA, map[string]string{AppVersionLabel: appV1})},
		"empty app":      {pod("p", podA, map[string]string{AppLabel: "", AppVersionLabel: appV1})},
		"empty version":  {pod("p", podA, map[string]string{AppLabel: appName, AppVersionLabel: ""})},
		"with agent":     {with(map[string]string{AgentLabel: "a1"})},
		"with workspace": {with(map[string]string{WorkspaceLabel: "ws"})},
		"with run":       {with(map[string]string{RunLabel: run1})},
		"agent version":  {agentVersion},
		"shared App IP":  {appPod("one", podA, appV1), appPod("two", podA, appV2)},
		"App and Agent":  {appPod("one", podA, appV1), agentPod("two", podA)},
		"invalid shares": {pod("one", podA, map[string]string{AppLabel: appName}), agentPod("two", podA)},
	} {
		t.Run(name, func(t *testing.T) {
			x := NewPodIndex()
			for _, p := range pods {
				x.Set(p)
			}
			sp, err := identify(t, x, podA, nil)
			if err == nil || sp.Pod != Unknown || sp.User != Unknown || sp.App != "" || sp.AppVersion != "" ||
				sp.Agent != "" || sp.Run != "" || sp.Namespace != "" {
				t.Fatalf("%v %+v", err, sp)
			}
		})
	}
}

func TestAppHeadersCannotChangeIdentity(t *testing.T) {
	x := NewPodIndex()
	p := appPod(shop0, podA, appV1)
	p.Labels[UserLabel] = alice
	x.Set(p)
	h := http.Header{}
	h.Set("X-Koodam-App", "other-app")
	h.Set("X-Koodam-App-Version", "other-version")
	h.Set("X-Koodam-Agent", "other-agent")
	sp, err := identify(t, x, podA, h)
	if err != nil || sp.App != appName || sp.AppVersion != appV1 || sp.Agent != "" || sp.User != alice ||
		sp.Claimed {
		t.Fatalf("%v %+v", err, sp)
	}
	h.Set(ClaimedRunHeader, "other-run")
	h.Set(ClaimedUserHeader, "other-user")
	sp, err = identify(t, x, podA, h)
	b, _ := json.Marshal(sp)
	if err != nil || sp.App != appName || sp.Run != "" || sp.User != alice || !sp.Claimed ||
		strings.Contains(string(b), "other") {
		t.Fatalf("%v %s", err, b)
	}
	// The headers do not give an identity to an unknown IP.
	if sp, err = identify(t, x, podB, h); err == nil || sp.App != "" || sp.Pod != Unknown {
		t.Fatalf("%v %+v", err, sp)
	}
}

func TestIndexFollowsTheAppPod(t *testing.T) {
	x := NewPodIndex()
	x.Set(appPod(shop0, podA, appV1))
	x.Set(appPod(shop0, podB, appV1))
	if _, err := identify(t, x, podA, nil); err == nil {
		t.Fatal("the old IP has an identity")
	}
	if sp, err := identify(t, x, podB, nil); err != nil || sp.AppVersion != appV1 {
		t.Fatalf("%v %+v", err, sp)
	}
	// A label change to a partial identity removes the identity.
	x.Set(pod(shop0, podB, map[string]string{AppLabel: appName}))
	if _, err := identify(t, x, podB, nil); err == nil {
		t.Fatal("a partial App identity is accepted")
	}
	ended := appPod(shop0, podB, appV1)
	ended.Status.Phase = corev1.PodFailed
	x.Set(ended)
	x.Set(appPod("shop-1", podA, appV2))
	if _, err := identify(t, x, podB, nil); err == nil {
		t.Fatal("an ended App pod has an identity")
	}
	x.Delete(ns, "shop-1")
	if _, err := identify(t, x, podA, nil); err == nil {
		t.Fatal("a deleted App pod has an identity")
	}
}

// TestFallbackSkipsNamedNamespaces sends events of the informers of all namespaces. They must not
// change a pod of a named namespace, and they must not give an App identity.
func TestFallbackSkipsNamedNamespaces(t *testing.T) {
	x := NewPodIndex()
	p := appPod(shop0, podA, appV1)
	x.Set(p)
	h := x.handler(map[string]bool{ns: true}, false)
	mixed := agentPod(shop0, podA)
	maps.Copy(mixed.Labels, p.Labels)
	h.OnAdd(mixed, false)
	h.OnUpdate(p, mixed)
	h.OnDelete(p)
	h.OnDelete(cache.DeletedFinalStateUnknown{Key: ns + "/" + shop0})
	if sp, err := identify(t, x, podA, nil); err != nil || sp.App != appName || sp.AppVersion != appV1 {
		t.Fatalf("an event of all namespaces changes a named namespace: %v %+v", err, sp)
	}
	// In an other namespace, the App pod stays in the index, thus its IP is not free.
	other := agentPod(shop0, podB)
	other.Namespace = prod
	maps.Copy(other.Labels, p.Labels)
	h.OnAdd(other, false)
	if sp, err := identify(t, x, podB, nil); !errors.Is(err, errAppLabels) || sp.App != "" {
		t.Fatalf("%v %+v", err, sp)
	}
	h.OnDelete(other)
	if _, err := identify(t, x, podB, nil); !errors.Is(err, errUnknownIP) {
		t.Fatalf("the deleted pod stays: %v", err)
	}
}

// eventually waits until ok is true.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWatchAppPods(t *testing.T) {
	inProd := appPod(shop0, podB, appV1)
	agentInProd := agentPod(pod1, podC)
	inProd.Namespace, agentInProd.Namespace = prod, prod
	cs := fake.NewClientset(appPod(shop0, podA, appV1), inProd, agentInProd)
	ctx := t.Context()
	// With no namespace list or with only NamespaceAll, the Agent pods have an identity and no
	// App pod has an identity.
	for _, namespaces := range [][]string{nil, {metav1.NamespaceAll}} {
		x := NewPodIndex()
		if err := x.Watch(ctx, cs, 0, namespaces...); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{podA, podB} {
			if sp, err := identify(t, x, source, nil); err == nil || sp.App != "" {
				t.Fatalf("%q: an App pod has an identity: %+v", namespaces, sp)
			}
		}
		if sp, err := identify(t, x, podC, nil); err != nil || sp.Namespace != prod || sp.Run != run1 {
			t.Fatalf("%q: %v %+v", namespaces, err, sp)
		}
	}
	// With NamespaceAll and a named namespace, only the named namespace gives App identities.
	x := NewPodIndex()
	if err := x.Watch(ctx, cs, 0, metav1.NamespaceAll, ns); err != nil {
		t.Fatal(err)
	}
	if sp, err := identify(t, x, podA, nil); err != nil || sp.App != appName || sp.AppVersion != appV1 {
		t.Fatalf("%v %+v", err, sp)
	}
	if sp, err := identify(t, x, podB, nil); err == nil || sp.App != "" {
		t.Fatalf("an App pod of an other namespace has an identity: %+v", sp)
	}
	if sp, err := identify(t, x, podC, nil); err != nil || sp.Namespace != prod || sp.Run != run1 {
		t.Fatalf("%v %+v", err, sp)
	}
}

// TestWatchAppLabelChanges changes the labels of the pods in a named namespace. The informers of
// all namespaces also run, and they must not change these pods.
func TestWatchAppLabelChanges(t *testing.T) {
	cs := fake.NewClientset(appPod(shop0, podA, appV1), agentPod(pod1, podB))
	ctx := t.Context()
	x := NewPodIndex()
	if err := x.Watch(ctx, cs, 0, ns, metav1.NamespaceAll); err != nil {
		t.Fatal(err)
	}
	pods := cs.CoreV1().Pods(ns)
	update := func(p *corev1.Pod) {
		t.Helper()
		if _, err := pods.Update(ctx, p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(what, source string, ok func(Span, error) bool) {
		t.Helper()
		eventually(t, what, func() bool { return ok(identify(t, x, source, nil)) })
	}
	// The Agent pod gets the App labels: a mixed pod has no identity.
	mixed := agentPod(pod1, podB)
	maps.Copy(mixed.Labels, map[string]string{AppLabel: appName, AppVersionLabel: appV1})
	update(mixed)
	check("a mixed pod has an identity", podB, func(_ Span, err error) bool { return err != nil })
	// It loses the Agent and run labels, and keeps the user label.
	update(pod(pod1, podB, map[string]string{AppLabel: appName, AppVersionLabel: appV1, UserLabel: alice}))
	check("the pod has no App identity", podB, func(sp Span, err error) bool {
		return err == nil && sp.App == appName && sp.User == alice && sp.Agent == "" && sp.Run == ""
	})
	// It loses the version label: a partial App identity.
	update(pod(pod1, podB, map[string]string{AppLabel: appName, UserLabel: alice}))
	check("a partial App identity is accepted", podB, func(_ Span, err error) bool { return err != nil })
	// It is an Agent pod again.
	update(agentPod(pod1, podB))
	check("the Agent pod has no identity", podB, func(sp Span, err error) bool {
		return err == nil && sp.Run == run1 && sp.App == ""
	})
	// A new version and a new IP.
	update(appPod(shop0, podC, appV2))
	check("the App pod has no new identity", podC, func(sp Span, err error) bool {
		return err == nil && sp.AppVersion == appV2
	})
	if _, err := identify(t, x, podA, nil); err == nil {
		t.Fatal("the App pod keeps its old IP")
	}
	// The App pod loses its App labels: no stale identity stays.
	update(pod(shop0, podC, map[string]string{"team": "shop"}))
	check("a stale App identity stays", podC, func(_ Span, err error) bool { return err != nil })
	if err := pods.Delete(ctx, pod1, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	check("the deleted pod stays in the index", podB, func(_ Span, err error) bool { return err != nil })
}
