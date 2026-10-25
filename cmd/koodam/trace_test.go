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

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	proxyNS   = "koodam-system"
	traceName = "run1"
)

// The spans of the fake proxy logs. Pod a has the spans 1 and 3, pod b has the span 2, so the
// sort by time must merge the 2 logs.
const (
	span1 = `{"v":2,"time":"2026-10-24T10:00:01Z","source":"http","kind":"llm","method":"POST",` +
		`"host":"gw","path":"/llm/v1/messages","status":200,"bytesIn":10,"bytesOut":20,"result":"ok",` +
		`"namespace":"dev","agent":"run1"}`
	span2 = `{"v":2,"time":"2026-10-24T10:00:02Z","source":"connect","kind":"tls","sni":"example.com",` +
		`"port":"443","bytesIn":5,"bytesOut":6,"result":"ok","namespace":"dev","agent":"run1"}`
	span3 = `{"v":2,"time":"2026-10-24T10:00:03Z","source":"http","kind":"http","method":"GET",` +
		`"host":"bad\u001b[31m.example","bytesIn":0,"bytesOut":0,"result":"denied","reason":"port",` +
		`"namespace":"dev","workspace":"run1"}`
	// Other spans that must not match: an other namespace, an other Agent.
	spanOtherNS    = `{"v":2,"time":"2026-10-24T10:00:01Z","result":"ok","namespace":"prod","agent":"run1"}`
	spanOtherAgent = `{"v":2,"time":"2026-10-24T10:00:01Z","result":"ok","namespace":"dev","agent":"run2"}`
)

// traceRun runs koodam with 2 fake proxy pods and returns the output and the log options of
// each pod.
func traceRun(t *testing.T, args ...string) (string, map[string]*corev1.PodLogOptions, error) {
	t.Helper()
	cs := kubefake.NewClientset()
	for _, name := range []string{"proxy-a", "proxy-b"} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: proxyNS, Labels: map[string]string{
			"app.kubernetes.io/name": "koodam", "app.kubernetes.io/component": "egress-proxy"}}}
		if _, err := cs.CoreV1().Pods(proxyNS).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	logs := map[string]string{
		"proxy-a": "egress proxy started\n" + span1 + "\n" + spanOtherNS + "\nnot json\n" + span3 + "\n",
		"proxy-b": spanOtherAgent + "\n" + span2 + "\n{\"msg\":\"no span\"}\n",
	}
	var mu sync.Mutex
	got := map[string]*corev1.PodLogOptions{}
	e := &env{clientset: cs, namespace: testNS, poll: time.Millisecond,
		stream: func(_ context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
			if ns != proxyNS {
				t.Errorf("the log of pod %s is read in namespace %s", pod, ns)
			}
			mu.Lock()
			got[pod] = opts
			mu.Unlock()
			return io.NopCloser(strings.NewReader(logs[pod])), nil
		}}
	root := newRoot(func() (*env, error) { return e, nil })
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"trace"}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), got, err
}

// TestTraceJSON checks that --json prints the lines of the target without change, sorted by time.
func TestTraceJSON(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want []string
	}{
		{traceName, []string{span1, span2, span3}},
		{"agent/" + traceName, []string{span1, span2}},
		{"workspace/" + traceName, []string{span3}},
		{"run3", nil},
	} {
		out, _, err := traceRun(t, tc.arg, "--json", "--proxy-namespace", proxyNS)
		if err != nil {
			t.Fatalf("%s: %v", tc.arg, err)
		}
		want := ""
		if tc.want != nil {
			want = strings.Join(tc.want, "\n") + "\n"
		}
		if out != want {
			t.Errorf("%s: the output is\n%s\nwant\n%s", tc.arg, out, want)
		}
	}
}

// TestTraceWindow checks since <= time < until, and that --since sets the log SinceTime.
func TestTraceWindow(t *testing.T) {
	out, opts, err := traceRun(t, traceName, "--json", "--since", "2026-10-24T10:00:02Z",
		"--until", "2026-10-24T10:00:03Z")
	if err != nil {
		t.Fatal(err)
	}
	if out != span2+"\n" {
		t.Errorf("the output is %q, want only span 2", out)
	}
	for pod, o := range opts {
		if o.SinceTime == nil || !o.SinceTime.Equal(&metav1.Time{Time: time.Date(2026, 10, 24, 10, 0, 2, 0, time.UTC)}) {
			t.Errorf("pod %s: SinceTime is %v", pod, o.SinceTime)
		}
		if o.Follow {
			t.Errorf("pod %s: Follow is true without --follow", pod)
		}
	}
	if len(opts) != 2 {
		t.Errorf("%d proxy logs are read, want 2", len(opts))
	}
}

// TestTraceTable checks the table and that a control character from the pod is not printed.
func TestTraceTable(t *testing.T) {
	out, _, err := traceRun(t, traceName)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "TIME") {
		t.Fatalf("the table is\n%s", out)
	}
	for i, want := range []string{"/llm/v1/messages", "example.com", "denied/port"} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("row %d has no %q: %s", i+1, want, lines[i+1])
		}
	}
	if strings.ContainsRune(out, '\x1b') || !strings.Contains(lines[3], "bad?[31m.example") {
		t.Errorf("the control character is not replaced: %q", lines[3])
	}
}

// TestTraceFollow checks that --follow streams each proxy pod log.
func TestTraceFollow(t *testing.T) {
	out, opts, err := traceRun(t, "agent/"+traceName, "--json", "-f")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != 2 || !strings.Contains(out, span1) || !strings.Contains(out, span2) {
		t.Errorf("the output is\n%s", out)
	}
	for pod, o := range opts {
		if !o.Follow {
			t.Errorf("pod %s: Follow is false with --follow", pod)
		}
	}
}

// TestTraceErrors checks the argument and flag errors, and the error with no proxy pod.
func TestTraceErrors(t *testing.T) {
	for _, args := range [][]string{
		{"job/run1"},
		{"agent/"},
		{"Bad\x1bName"},
		{traceName, "--since", "yesterday"},
		{traceName, "--until", "2026-10-24"},
		{traceName, "--proxy-namespace", "default"},
	} {
		if _, _, err := traceRun(t, args...); err == nil {
			t.Errorf("%q: no error", args)
		} else if strings.ContainsRune(err.Error(), '\x1b') {
			t.Errorf("%q: the error shows the argument", args)
		}
	}
}
