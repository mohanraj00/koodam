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
	"errors"
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
	proxyA    = "proxy-a"
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
	// Other spans that must not match: an other namespace, an other Agent, an other version.
	spanOtherNS    = `{"v":2,"time":"2026-10-24T10:00:01Z","result":"ok","namespace":"prod","agent":"run1"}`
	spanOtherAgent = `{"v":2,"time":"2026-10-24T10:00:01Z","result":"ok","namespace":"dev","agent":"run2"}`
	spanV1         = `{"v":1,"time":"2026-10-24T10:00:01Z","result":"ok","namespace":"dev","agent":"run1"}`
)

// logStream is the fake log stream of a proxy pod.
type logStream func(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)

// traceRun runs koodam with 2 fake proxy pods and returns the output and the log options of
// each pod.
func traceRun(t *testing.T, args ...string) (string, map[string]*corev1.PodLogOptions, error) {
	t.Helper()
	logs := map[string]string{
		proxyA:    "egress proxy started\n" + span1 + "\n" + spanOtherNS + "\nnot json\n" + span3 + "\n",
		"proxy-b": spanOtherAgent + "\n" + span2 + "\n{\"msg\":\"no span\"}\n" + spanV1 + "\n",
	}
	var mu sync.Mutex
	got := map[string]*corev1.PodLogOptions{}
	out := &bytes.Buffer{}
	err := traceExec(t, out, func(_ context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
		if ns != proxyNS {
			t.Errorf("the log of pod %s is read in namespace %s", pod, ns)
		}
		mu.Lock()
		got[pod] = opts
		mu.Unlock()
		return io.NopCloser(strings.NewReader(logs[pod])), nil
	}, args...)
	return out.String(), got, err
}

// traceExec runs koodam trace with 2 fake proxy pods, the log stream and the output.
func traceExec(t *testing.T, out io.Writer, stream logStream, args ...string) error {
	t.Helper()
	cs := kubefake.NewClientset()
	for _, name := range []string{proxyA, "proxy-b"} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: proxyNS, Labels: map[string]string{
			"app.kubernetes.io/name": "koodam", "app.kubernetes.io/component": "egress-proxy"}}}
		if _, err := cs.CoreV1().Pods(proxyNS).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{clientset: cs, namespace: testNS, poll: time.Millisecond, stream: stream}
	root := newRoot(func() (*env, error) { return e, nil })
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"trace"}, args...))
	return root.ExecuteContext(context.Background())
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

// TestTraceFollowError checks that a failed stream stops the other streams with --follow, and
// that the command gives the error.
func TestTraceFollowError(t *testing.T) {
	done := make(chan struct{})
	stream := func(ctx context.Context, _, pod string, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
		if pod == proxyA {
			// This stream stays open until the command cancels it.
			r, w := io.Pipe()
			go func() {
				<-ctx.Done()
				_ = w.CloseWithError(ctx.Err())
				close(done)
			}()
			return r, nil
		}
		return io.NopCloser(strings.NewReader(strings.Repeat("x", maxSpanLine+1))), nil
	}
	err := traceExec(t, &bytes.Buffer{}, stream, traceName, "-f")
	if err == nil || !strings.Contains(err.Error(), "pod/proxy-b") {
		t.Errorf("the error is %v, want the error of pod/proxy-b", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the stream of proxy-a is not cancelled")
	}
}

// errWriter is an output that fails.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, io.ErrShortWrite }

// TestTraceOutputError checks that an output error is the result, with and without --follow.
func TestTraceOutputError(t *testing.T) {
	stream := func(context.Context, string, string, *corev1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(span1 + "\n")), nil
	}
	for _, args := range [][]string{{traceName, "--json"}, {traceName}, {traceName, "--json", "-f"}} {
		if err := traceExec(t, errWriter{}, stream, args...); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// TestTraceTooLarge checks the size limit of the spans for the sort.
func TestTraceTooLarge(t *testing.T) {
	old := maxSortBytes
	maxSortBytes = len(span1) + len(span2)
	defer func() { maxSortBytes = old }()
	if _, _, err := traceRun(t, traceName, "--json"); !errors.Is(err, errTooLarge) {
		t.Errorf("the error is %v, want errTooLarge", err)
	}
	if _, _, err := traceRun(t, "agent/"+traceName, "--json"); err != nil {
		t.Errorf("2 spans at the limit: %v", err)
	}
}
