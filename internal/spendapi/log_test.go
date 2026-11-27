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
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const logPath = testPath + "/" + logPart

// fakeLogs records the options of the last call and gives a fixed log.
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
	return io.NopCloser(strings.NewReader(l.data)), l.err
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
		"end of the run",
	}, "\n") + "\n"
	w := f.call(t, http.MethodGet, logPath, f.reader, http.StatusOK)
	body := w.Body.String()
	for _, secret := range []string{key.value, key.value[:16], key.hash, f.reader, jwt, login} {
		if strings.Contains(body, secret) {
			t.Fatalf("the log response has a credential: %s", body)
		}
	}
	l := decode[runLog](t, w)
	if l.Pod != podName || l.Container != agentContainer || len(l.Lines) != 9 || l.Lines[0] != "start of the run" ||
		l.Lines[8] != "end of the run" || l.Lines[2] != "the gateway key is [redacted]" || l.Truncated {
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
	if o.Container != agentContainer || *o.TailLines != defaultLogLines || *o.LimitBytes != maxLogRead+1 || logs.pod != podName {
		t.Fatalf("the log options do not match: %+v", o)
	}
	if len(l.Lines) != defaultLogLines || l.Lines[0] != "line 100" || !l.Truncated {
		t.Fatal("want the last 200 lines")
	}
	f.call(t, http.MethodGet, logPath+"?lines=1000", f.reader, http.StatusOK)
	if *logs.options.TailLines != maxLogLines {
		t.Fatal("the lines query is not in the options")
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

var wholeLine = regexp.MustCompile(`^line-\d{7}-x{20}$`)

func TestLogByteLimitKeepsWholeLines(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < 2*maxLogRead; i++ {
		fmt.Fprintf(&b, "line-%07d-%s\n", i, strings.Repeat("x", 20))
	}
	// The kubelet cut is one byte after maxLogRead. A stream that starts in a line is the third case.
	front := b.String()[3 : maxLogBytes+50]
	front = front[:strings.LastIndexByte(front, '\n')+1]
	for _, data := range []string{b.String(), b.String()[:maxLogRead+1], front} {
		got, truncated := tailLines([]byte(data), maxLogLines)
		size := 0
		for _, line := range got {
			size += len(line) + 1
			if !wholeLine.MatchString(line) {
				t.Fatalf("the result has a part of a line: %q", line)
			}
		}
		if !truncated || size > maxLogBytes || len(got) == 0 {
			t.Fatalf("want a truncated tail of whole lines: %d lines, %d bytes", len(got), size)
		}
	}
	if got, truncated := tailLines([]byte(strings.Repeat("x", maxLogBytes+1)), 10); len(got) != 0 || !truncated {
		t.Fatal("a long line with no end is not whole")
	}
	if got, _ := tailLines(nil, 10); got == nil || len(got) != 0 {
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
