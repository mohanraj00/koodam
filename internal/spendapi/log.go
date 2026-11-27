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
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	logPart = "log"
	// defaultLogLines and maxLogLines are the default and the maximum of the lines query.
	defaultLogLines = 200
	maxLogLines     = 1000
	// maxLogBytes limits the log bytes of a response, before the redaction.
	maxLogBytes = 256 << 10
	// maxLogRead limits the bytes that the server reads from the API server for one request.
	maxLogRead = 4 * maxLogBytes
)

var errNoPod = errors.New("the run has no pod")

// LogSource opens the log of one container of a pod. The server gives the line limit and the
// byte limit in the options, and it reads no more than the byte limit.
type LogSource func(ctx context.Context, namespace, pod string, options *corev1.PodLogOptions) (io.ReadCloser, error)

type runLog struct {
	Pod       string   `json:"pod"`
	Container string   `json:"container"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// serveLog gives the last lines of the agent container of the newest pod of a run. The log path
// needs the viewer mode, because only that mode can check get on pods/log for the caller.
func (a *api) serveLog(ctx context.Context, w http.ResponseWriter, r *http.Request, ref runRef) {
	if a.logs == nil {
		reply(w, r, http.StatusNotFound, problem{"This path does not exist."})
		return
	}
	if a.viewers == nil {
		reply(w, r, http.StatusForbidden, problem{"The log path needs the viewer review mode."})
		return
	}
	lines, ok := logLines(r.URL.RawQuery)
	if !ok {
		reply(w, r, http.StatusBadRequest, problem{"Use only the query lines, from 1 to 1000."})
		return
	}
	result, err := a.runLog(ctx, ref, lines)
	switch {
	case ctx.Err() != nil:
		reply(w, r, http.StatusGatewayTimeout, problem{msgTimeLimit})
	case apierrors.IsNotFound(err):
		reply(w, r, http.StatusNotFound, problem{"The run does not exist."})
	case errors.Is(err, errNoPod):
		reply(w, r, http.StatusNotFound, problem{"The run has no pod."})
	case apierrors.IsBadRequest(err):
		reply(w, r, http.StatusConflict, problem{"The container has no log yet."})
	case err != nil:
		reply(w, r, http.StatusBadGateway, problem{"The log is not available."})
	default:
		reply(w, r, http.StatusOK, result)
	}
}

func logLines(query string) (int, bool) {
	q, err := url.ParseQuery(query)
	if err != nil || len(q) > 1 || (len(q) == 1 && len(q["lines"]) != 1) {
		return 0, false
	}
	if !q.Has("lines") {
		return defaultLogLines, true
	}
	n, err := strconv.Atoi(q.Get("lines"))
	return n, err == nil && n > 0 && n <= maxLogLines
}

func (a *api) runLog(ctx context.Context, ref runRef, lines int) (runLog, error) {
	agent := &corev1alpha1.Agent{}
	if err := a.kube.Get(ctx, client.ObjectKey{Namespace: ref.namespace, Name: ref.name}, agent); err != nil {
		return runLog{}, err
	}
	_, pods, err := a.runPods(ctx, agent)
	if err != nil {
		return runLog{}, err
	}
	if len(pods) == 0 || len(pods[0].Spec.Containers) == 0 {
		return runLog{}, errNoPod
	}
	pod := &pods[0]
	result := runLog{Pod: pod.Name, Container: pod.Spec.Containers[0].Name}
	tail, limit := int64(lines), int64(maxLogRead+1)
	stream, err := a.logs(ctx, pod.Namespace, pod.Name,
		&corev1.PodLogOptions{Container: result.Container, TailLines: &tail, LimitBytes: &limit})
	if err != nil {
		return runLog{}, err
	}
	defer func() { _ = stream.Close() }()
	data, err := io.ReadAll(io.LimitReader(stream, maxLogRead+1))
	if err != nil {
		return runLog{}, err
	}
	result.Lines, result.Truncated = tailLines(data, lines)
	result.Lines = redactLines(result.Lines)
	return result, nil
}

// tailLines gives the last whole lines of data, with no more than maxLogBytes and count lines. Each
// cut drops the part of the line at the cut, so no line in the result is a part of a line. Thus a
// cut cannot leave a part of a credential that a pattern does not match.
func tailLines(data []byte, count int) ([]string, bool) {
	truncated := false
	if len(data) > maxLogRead {
		data, truncated = data[:bytes.LastIndexByte(data[:maxLogRead], '\n')+1], true
	}
	if len(data) > maxLogBytes {
		data, truncated = data[len(data)-maxLogBytes:], true
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return []string{}, truncated
	}
	result := strings.Split(text, "\n")
	if len(result) > count {
		result, truncated = result[len(result)-count:], true
	}
	return result, truncated
}
