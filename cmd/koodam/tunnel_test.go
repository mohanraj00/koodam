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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"
)

const (
	tunnelNS     = "dev-mohan"
	tunnelTarget = tunnelNS + "/main"
	sshBanner    = "SSH-2.0-OpenSSH_9.9\r\n"
)

// fakeStream is one stream of a fake port-forward connection.
type fakeStream struct {
	io.Reader
	io.Writer
	headers http.Header
	close   func() error
	reset   func() error
}

func (s *fakeStream) Close() error         { return s.close() }
func (s *fakeStream) Reset() error         { return s.reset() }
func (s *fakeStream) Headers() http.Header { return s.headers }
func (s *fakeStream) Identifier() uint32   { return 0 }
func (s *fakeStream) Write(p []byte) (int, error) {
	if s.Writer == nil {
		return 0, errors.New("read-only stream")
	}
	return s.Writer.Write(p)
}

// fakeConn is an in-process port-forward. The remote side sends the SSH banner, reads the
// client data to the end, and then ends. errMsg is the text on the error stream.
type fakeConn struct {
	errMsg string
	banner string
	// errBlock, if set, keeps the error stream open with no data.
	errBlock bool
	closed   chan bool
	once     sync.Once
	got      bytes.Buffer
	headers  []http.Header
	done     chan struct{}
}

func newFakeConn(errMsg string) *fakeConn {
	return &fakeConn{errMsg: errMsg, banner: sshBanner, closed: make(chan bool), done: make(chan struct{})}
}

func (c *fakeConn) CreateStream(h http.Header) (httpstream.Stream, error) {
	c.headers = append(c.headers, h.Clone())
	nop := func() error { return nil }
	if h.Get(corev1.StreamType) == corev1.StreamTypeError && c.errBlock {
		r, _ := io.Pipe()
		return &fakeStream{Reader: r, headers: h, close: nop, reset: nop}, nil
	}
	if h.Get(corev1.StreamType) == corev1.StreamTypeError {
		return &fakeStream{Reader: strings.NewReader(c.errMsg), headers: h, close: nop, reset: nop}, nil
	}
	fromRemote, remoteOut := io.Pipe()
	remoteIn, toRemote := io.Pipe()
	go func() {
		defer close(c.done)
		_, _ = remoteOut.Write([]byte(c.banner))
		_, _ = io.Copy(&c.got, remoteIn)
		_ = remoteOut.Close()
	}()
	return &fakeStream{Reader: fromRemote, Writer: toRemote, headers: h,
		close: toRemote.Close,
		reset: func() error {
			_ = toRemote.Close()
			return fromRemote.Close()
		}}, nil
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *fakeConn) CloseChan() <-chan bool             { return c.closed }
func (c *fakeConn) SetIdleTimeout(time.Duration)       {}
func (c *fakeConn) RemoveStreams(...httpstream.Stream) {}

func workspacePod(phase corev1.PodPhase) runtime.Object {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "main-0", Namespace: tunnelNS},
		Status: corev1.PodStatus{Phase: phase}}
}

// runTunnel runs "koodam tunnel" with stdin in. It returns stdout and stderr apart.
func runTunnel(t *testing.T, dial dialFunc, in string, arg string, objs ...runtime.Object) (string, string, error) {
	t.Helper()
	e := &env{clientset: kubefake.NewClientset(objs...), namespace: testNS, dial: dial}
	root := newRoot(func() (*env, error) { return e, nil })
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetIn(strings.NewReader(in))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"tunnel", arg})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root.SetContext(ctx)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// TestTunnelArgs checks that a bad argument fails before the connection, and that the error
// does not show control characters.
func TestTunnelArgs(t *testing.T) {
	for _, arg := range []string{"", "main", "a/b/c", "/main", "dev/", "Dev/main", "dev/-main",
		"dev/ma_in", "dev/main\x1b[31m", "dev\n/main", strings.Repeat("a", 64) + "/main"} {
		dialed := false
		dial := func(context.Context, string, string) (httpstream.Connection, error) {
			dialed = true
			return nil, errors.New("no dial")
		}
		out, errOut, err := runTunnel(t, dial, "", arg, workspacePod(corev1.PodRunning))
		if err == nil || dialed {
			t.Errorf("%q: got err %v, dialed %v, want an error before the dial", arg, err, dialed)
		}
		if out != "" {
			t.Errorf("%q: stdout %q, want nothing", arg, out)
		}
		if strings.ContainsAny(errOut, "\x1b\r") || strings.Contains(errOut, "\n/") {
			t.Errorf("%q: stderr shows a control character: %q", arg, errOut)
		}
	}
}

// TestTunnelData checks that stdout gets only the remote data, that the remote side gets
// stdin, and that the streams ask for port 22.
func TestTunnelData(t *testing.T) {
	c := newFakeConn("")
	var gotNS, gotPod string
	dial := func(_ context.Context, ns, pod string) (httpstream.Connection, error) {
		gotNS, gotPod = ns, pod
		return c, nil
	}
	out, errOut, err := runTunnel(t, dial, "SSH-2.0-client\r\n", tunnelTarget, workspacePod(corev1.PodRunning))
	if err != nil {
		t.Fatalf("tunnel: %v (stderr %q)", err, errOut)
	}
	<-c.done
	if out != sshBanner {
		t.Errorf("stdout %q, want only %q", out, sshBanner)
	}
	if errOut != "" {
		t.Errorf("stderr %q, want nothing", errOut)
	}
	if c.got.String() != "SSH-2.0-client\r\n" {
		t.Errorf("the remote side got %q", c.got.String())
	}
	if gotNS != tunnelNS || gotPod != "main-0" {
		t.Errorf("dial %s/%s, want %s/main-0", gotNS, gotPod, tunnelNS)
	}
	if len(c.headers) != 2 {
		t.Fatalf("got %d streams, want 2", len(c.headers))
	}
	for i, want := range []string{corev1.StreamTypeError, corev1.StreamTypeData} {
		if h := c.headers[i]; h.Get(corev1.StreamType) != want || h.Get(corev1.PortHeader) != "22" {
			t.Errorf("stream %d headers %v, want type %s and port 22", i, h, want)
		}
	}
}

// TestTunnelErrors checks that each failure gives a non-zero exit, an error on stderr and
// nothing on stdout.
func TestTunnelErrors(t *testing.T) {
	remoteErr := newFakeConn("connection refused\x1b")
	remoteErr.banner = ""
	cases := []struct {
		name, want string
		conn       *fakeConn
		dialErr    error
		pod        runtime.Object
	}{
		{name: "no pod", want: "check the Workspace main"},
		{name: "pod not running", want: "is Pending, not Running", pod: workspacePod(corev1.PodPending)},
		{name: "dial fails", want: "you need create on pods/portforward", pod: workspacePod(corev1.PodRunning),
			dialErr: errors.New("the API server denied the port-forward (HTTP 403): you need create on pods/portforward")},
		{name: "remote error", want: "port-forward to port 22 failed: connection refused",
			pod: workspacePod(corev1.PodRunning), conn: remoteErr},
	}
	for _, tc := range cases {
		dial := func(context.Context, string, string) (httpstream.Connection, error) {
			if tc.dialErr != nil {
				return nil, tc.dialErr
			}
			return tc.conn, nil
		}
		var objs []runtime.Object
		if tc.pod != nil {
			objs = append(objs, tc.pod)
		}
		out, errOut, err := runTunnel(t, dial, "", tunnelTarget, objs...)
		if err == nil {
			t.Errorf("%s: want an error", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(strings.Fields(errOut), " "), tc.want) {
			t.Errorf("%s: stderr %q, want %q", tc.name, errOut, tc.want)
		}
		if out != "" {
			t.Errorf("%s: stdout %q, want nothing", tc.name, out)
		}
		if strings.Contains(errOut, "\x1b") {
			t.Errorf("%s: stderr shows a control character: %q", tc.name, errOut)
		}
	}
}

// TestSpdyDialErrors checks that a dial error does not show the token or the server address.
func TestSpdyDialErrors(t *testing.T) {
	const token = "token-value-1234"
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"forbidden ` + token + `"}`))
	}))
	cfg := &rest.Config{Host: srv.URL, BearerToken: token}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = spdyDial(cfg, cs)(context.Background(), tunnelNS, "main-0")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("got %v, want a 403 error", err)
	}
	if gotPath != "/api/v1/namespaces/dev-mohan/pods/main-0/portforward" || gotAuth != "Bearer "+token {
		t.Errorf("request path %q, auth set %v", gotPath, gotAuth != "")
	}
	srv.Close()
	_, err2 := spdyDial(cfg, cs)(context.Background(), tunnelNS, "main-0")
	for _, e := range []error{err, err2} {
		if e == nil {
			t.Fatal("want an error after the server stops")
		}
		if msg := e.Error(); strings.Contains(msg, token) || strings.Contains(msg, srv.URL) {
			t.Errorf("the error shows the token or the server address: %q", msg)
		}
	}
}

// errReader is a stdin that fails.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin failed") }

// TestTunnelStdinError checks that a stdin error ends the tunnel when the error stream stays open.
func TestTunnelStdinError(t *testing.T) {
	old := errWait
	errWait = 50 * time.Millisecond
	defer func() { errWait = old }()
	c := newFakeConn("")
	c.banner = ""
	c.errBlock = true
	done := make(chan error, 1)
	go func() { done <- forward(context.Background(), c, errReader{}, io.Discard) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stdin failed") {
			t.Errorf("got %v, want the stdin error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forward did not end after a stdin error")
	}
}
