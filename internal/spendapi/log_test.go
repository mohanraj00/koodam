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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const logPath = testPath + "/" + logPart

// fakeLogs records the options of the last call. It applies TailLines and then LimitBytes.
type fakeLogs struct {
	data    string
	err     error
	options *corev1.PodLogOptions
	pod     string
}

func (l *fakeLogs) open(_ context.Context, namespace, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	if namespace != testNamespace {
		return nil, errors.New("the log of a different namespace")
	}
	l.options, l.pod = o, pod
	data := l.data
	if lines := strings.SplitAfter(data, "\n"); o.TailLines != nil && int64(len(lines)-1) > *o.TailLines {
		data = strings.Join(lines[len(lines)-1-int(*o.TailLines):], "")
	}
	if o.LimitBytes != nil && int64(len(data)) > *o.LimitBytes {
		data = data[:*o.LimitBytes]
	}
	return io.NopCloser(strings.NewReader(data)), l.err
}

func runObjectsWithSpec(hash string) []client.Object {
	objects := runObjects(hash)
	objects[2].(*corev1.Pod).Spec.Containers = []corev1.Container{{Name: agentContainer}, {Name: "proxy"}}
	return objects
}

func newLogFixture(t *testing.T, data string) (*viewerFixture, *fakeLogs) {
	t.Helper()
	f := newViewerFixture(t)
	f.kube = runClient(t, runObjectsWithSpec(f.runKeys[0].hash)...)
	logs := &fakeLogs{data: data}
	f.logs = logs.open
	return f, logs
}

// TestLogHasNoCredential is the test of completion criterion 3 of #399: a log response has no key
// value, no key hash, no token and no login value.
func TestLogHasNoCredential(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJzeXN0ZW06c2EifQ.c2lnbmF0dXJlMDEyMzQ1"
	f, logs := newLogFixture(t, "")
	key := f.runKeys[0]
	login := "hunter2-login-value"
	logs.data = strings.Join([]string{
		"start of the run",
		"OPENAI_API_KEY=" + key.value,
		"the gateway key is " + key.value,
		`{"keyHash": "` + key.hash + `"}`,
		"Authorization: Bearer " + f.reader,
		"service account token " + jwt,
		"clone https://alice:" + login + "@git.example.com/repo.git",
		"login=" + login,
		`{"password":"x\"` + login + `"}`,
		"-----BEGIN \x1b[1mPRIVATE KEY-----",
		keyBody,
		"-----END PRIVATE KEY-----",
		"end of the run",
	}, "\n") + "\n"
	w := f.call(t, http.MethodGet, logPath, f.reader, http.StatusOK)
	body := w.Body.String()
	for _, secret := range []string{key.value, key.value[:16], key.hash, f.reader, jwt, login, keyBody} {
		if strings.Contains(body, secret) {
			t.Fatalf("the log response has a credential: %s", body)
		}
	}
	l := decode[runLog](t, w)
	if l.Pod != podName || l.Container != agentContainer || len(l.Lines) != 13 || l.Lines[0] != "start of the run" ||
		l.Lines[12] != "end of the run" || l.Lines[2] != "the gateway key is [redacted]" || l.Truncated {
		t.Fatalf("the log does not match: %+v", l)
	}
}

func TestLogLimitsAndReviews(t *testing.T) {
	lines := make([]string, 0, 300)
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	f, logs := newLogFixture(t, strings.Join(lines, "\n")+"\n")
	l := decode[runLog](t, f.call(t, http.MethodGet, logPath, f.reader, http.StatusOK))
	o := logs.options
	if o.Container != agentContainer || *o.TailLines != defaultLogLines+keyContext+1 || *o.LimitBytes != maxLogRead+1 || logs.pod != podName {
		t.Fatalf("the log options do not match: %+v", o)
	}
	if len(l.Lines) != defaultLogLines || l.Lines[0] != "line 100" || !l.Truncated {
		t.Fatal("want the last 200 lines")
	}
	f.call(t, http.MethodGet, logPath+"?lines=1000", f.reader, http.StatusOK)
	if *logs.options.TailLines != maxLogLines+keyContext+1 {
		t.Fatal("the lines query is not in the options")
	}
	logs.data = strings.Join(lines[:defaultLogLines], "\n") + "\n"
	if l := decode[runLog](t, f.call(t, http.MethodGet, logPath, f.reader, http.StatusOK)); len(l.Lines) != defaultLogLines || l.Truncated {
		t.Fatal("a log of exactly 200 lines is not truncated")
	}
	for _, q := range []string{"?lines=0", "?lines=1001", "?lines=x", "?lines=1&lines=2", "?follow=true"} {
		f.call(t, http.MethodGet, logPath+q, f.reader, http.StatusBadRequest)
	}
	f.mu.Lock()
	checks := make([]string, 0, len(f.reviews))
	for _, r := range f.reviews {
		a := r.ResourceAttributes
		checks = append(checks, a.Namespace+" "+a.Verb+" "+a.Resource+"/"+a.Subresource)
	}
	f.mu.Unlock()
	if !strings.Contains(strings.Join(checks, ","), testNamespace+" get pods/log") ||
		strings.Contains(strings.Join(checks, ","), otherNamespace) {
		t.Fatalf("want the review of get pods/log in the run namespace only: %v", checks)
	}
	f.denyLog = true
	f.call(t, http.MethodGet, logPath, f.reader, http.StatusForbidden)
	f.call(t, http.MethodGet, testPath, f.reader, http.StatusOK)
}

func TestLogReadLimitRefusesOversizedWindow(t *testing.T) {
	line := strings.Repeat("x", (4<<10)-1) + "\n"
	data := strings.Repeat(line, defaultLogLines+keyContext) + "newest line\n"
	f, _ := newLogFixture(t, data)
	w := f.call(t, http.MethodGet, logPath, f.reader, http.StatusUnprocessableEntity)
	if got := decode[problem](t, w); got.Error != "The log window is larger than the read limit. Ask for fewer lines." {
		t.Fatalf("want a log read limit error, got %+v", got)
	}
}

func TestLogByteLimitKeepsNewestWholeLines(t *testing.T) {
	const lineBytes = 4 << 10
	padding := strings.Repeat("x", lineBytes-len("line-000-\n"))
	lines := make([]string, maxLogRead/lineBytes)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d-%s", i, padding)
	}
	f, _ := newLogFixture(t, strings.Join(lines, "\n")+"\n")
	l := decode[runLog](t, f.call(t, http.MethodGet, logPath, f.reader, http.StatusOK))
	if want := lines[len(lines)-maxLogBytes/lineBytes:]; !l.Truncated || !slices.Equal(l.Lines, want) {
		t.Fatal("want the newest whole lines that fit in the response")
	}
	if got, truncated := tailLines([]byte(strings.Repeat("x", maxLogBytes+1)), 10, 20); len(got) != 0 || !truncated {
		t.Fatal("a long line with no end is not whole")
	}
	if got, _ := tailLines(nil, 10, 20); got == nil || len(got) != 0 {
		t.Fatal("want an empty list for an empty log")
	}
}

func TestLogRefusalsAndErrors(t *testing.T) {
	f, logs := newLogFixture(t, "line\n")
	logs.err = apierrors.NewBadRequest("container is waiting to start")
	f.call(t, http.MethodGet, logPath, f.reader, http.StatusConflict)
	logs.err = errors.New("the kubelet is not available")
	f.call(t, http.MethodGet, logPath, f.reader, http.StatusBadGateway)
	logs.err = nil
	f.call(t, http.MethodGet, runPrefix+testNamespace+"/missing/log", f.reader, http.StatusNotFound)
	f.call(t, http.MethodHead, logPath, f.reader, http.StatusOK)
	f.call(t, http.MethodGet, logPath, "", http.StatusUnauthorized)
	f.call(t, http.MethodGet, runPrefix+otherNamespace+"/"+runName+"/log", f.reader, http.StatusForbidden)
	f.kube = runClient(t, runObjects(f.runKeys[0].hash)[0])
	f.call(t, http.MethodGet, logPath, f.reader, http.StatusNotFound)

	s := newFixture(t, 0)
	s.kube = runClient(t, runObjectsWithSpec(s.runKeys[0].hash)...)
	s.call(t, http.MethodGet, logPath, s.token, http.StatusNotFound)
	s.logs = logs.open
	s.call(t, http.MethodGet, logPath, s.token, http.StatusForbidden)
}

// TestLogTailInAKey shows that a line limit that cuts a private key leaves no part of the key.
func TestLogTailInAKey(t *testing.T) {
	f, logs := newLogFixture(t, "")
	logs.data = "before\n-----BEGIN PRIVATE KEY-----\n" + keyBody + "\nc2Vjb25kIGxpbmU=\n" +
		"-----END PRIVATE KEY-----\nafter\n"
	l := decode[runLog](t, f.call(t, http.MethodGet, logPath+"?lines=4", f.reader, http.StatusOK))
	if want := []string{redacted, redacted, redacted, afterKey}; !slices.Equal(l.Lines, want) {
		t.Fatalf("the log lines are %q, want %q", l.Lines, want)
	}
}

// TestLogTailInAnOpenKey shows a tail in a key that has no END line yet. If the BEGIN line is in the
// lines before the tail, the redaction finds it. If it is not, the lines at the start of the window
// that can be the body of a key are redacted.
func TestLogTailInAnOpenKey(t *testing.T) {
	f, logs := newLogFixture(t, "")
	body := strings.Repeat(keyBody+"\n", 400)
	for data, want := range map[string][]string{
		"before\n-----BEGIN PRIVATE KEY-----\n" + strings.Repeat(keyBody+"\n", 30): {redacted, redacted, redacted},
		"-----BEGIN PRIVATE KEY-----\n" + body:                                     {redacted, redacted, redacted},
		"-----BEGIN PRIVATE KEY-----\n" + body + "after\n":                         {redacted, redacted, afterKey},
	} {
		logs.data = data
		l := decode[runLog](t, f.call(t, http.MethodGet, logPath+"?lines=3", f.reader, http.StatusOK))
		if !slices.Equal(l.Lines, want) || !l.Truncated {
			t.Fatalf("the log lines are %q, want %q", l.Lines, want)
		}
	}
}
