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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

const (
	watchPart = "watch"
	// maxWatchTime limits the time of one watch. Then the client connects again, and the server
	// does the token review and the access review again.
	maxWatchTime = 5 * time.Minute
	// maxOpenWatches limits the open watches of the server.
	maxOpenWatches = 100
	// maxCallerWatches limits the open watches of one caller, so one caller cannot use all the
	// watches of the server.
	maxCallerWatches = 10
	// watchRecheck is the interval of the token review and the access review during a watch.
	watchRecheck = time.Minute
	// watchPing is the interval of a comment line that keeps the connection open.
	watchPing = 15 * time.Second
	// watchWriteTime limits each write, so a client that does not read cannot keep a watch.
	watchWriteTime = 2 * watchPing
	// watchRetry is the time in milliseconds that a client waits before it connects again.
	watchRetry = "5000"
	// eventStatus is the name of the event with the status of the Agent.
	eventStatus = "status"
)

// AgentWatcher starts a watch of Agents. A client.WithWatch has this method.
type AgentWatcher interface {
	Watch(ctx context.Context, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error)
}

// runStatus is the data of a status event. It has the Agent status only. A client reads the Job
// and the pods from the run path when the phase changes.
type runStatus struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Namespace   string      `json:"namespace"`
	Phase       string      `json:"phase,omitempty"`
	StartedAt   *time.Time  `json:"startedAt,omitempty"`
	CompletedAt *time.Time  `json:"completedAt,omitempty"`
	Conditions  []condition `json:"conditions"`
}

func statusOf(agent *corev1beta1.Agent) runStatus {
	s := runStatus{
		ID: agent.Status.RunID, Name: agent.Name, Namespace: agent.Namespace, Phase: string(agent.Status.Phase),
		StartedAt: utc(agent.Status.StartedAt), CompletedAt: utc(agent.Status.CompletedAt),
		Conditions: conditions(agent.Status.Conditions),
	}
	if s.ID == "" {
		s.ID = string(agent.UID)
	}
	return s
}

// openWatch counts a new watch of the caller, or gives the problem of the refusal.
func (a *api) openWatch(caller string) (problem, bool) {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	switch {
	case a.watches.Load() >= maxOpenWatches:
		return problem{"Too many watches are open. Try again later."}, false
	case a.callerWatches[caller] >= maxCallerWatches:
		return problem{"The caller has too many open watches. Try again later."}, false
	}
	if a.callerWatches == nil {
		a.callerWatches = map[string]int{}
	}
	a.watches.Add(1)
	a.callerWatches[caller]++
	return problem{}, true
}

func (a *api) closeWatch(caller string) {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	a.watches.Add(-1)
	if a.callerWatches[caller]--; a.callerWatches[caller] <= 0 {
		delete(a.callerWatches, caller)
	}
}

// checkAccess does the token review and the access review of the watch path again. It gives the
// reason of the end event, or "" if the caller can continue.
func (a *api) checkAccess(ctx context.Context, r *http.Request, ref runRef) string {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	_, _, status, _ := a.scope(ctx, r, []string{ref.namespace}, []authorizationv1.ResourceAttributes{listAgents})
	switch status {
	case http.StatusOK:
		return ""
	case http.StatusUnauthorized, http.StatusForbidden:
		return "accessEnded"
	default:
		return "accessCheckFailed"
	}
}

// serveWatch sends a status event when the watch starts and after each change of the status, as
// server-sent events. It ends with an end event or a deleted event.
func (a *api) serveWatch(ctx context.Context, w http.ResponseWriter, r *http.Request, ref runRef, caller string) {
	if a.watcher == nil {
		reply(w, r, http.StatusNotFound, problem{msgNoPath})
		return
	}
	if r.URL.RawQuery != "" {
		reply(w, r, http.StatusBadRequest, problem{msgNoQuery})
		return
	}
	if refusal, ok := a.openWatch(caller); !ok {
		w.Header().Set("Retry-After", "5")
		reply(w, r, http.StatusTooManyRequests, refusal)
		return
	}
	defer a.closeWatch(caller)
	agent := &corev1beta1.Agent{}
	err := a.kube.Get(ctx, client.ObjectKey{Namespace: ref.namespace, Name: ref.name}, agent)
	var stream watch.Interface
	watchCtx, cancel := context.WithTimeout(r.Context(), a.watchTime)
	defer cancel()
	if err == nil && r.Method == http.MethodGet {
		// The stream lives in watchCtx, but the request time limit also limits the start of the watch.
		stop := context.AfterFunc(ctx, cancel)
		stream, err = a.watcher.Watch(watchCtx, &corev1beta1.AgentList{}, client.InNamespace(ref.namespace),
			&client.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", ref.name),
				Raw: &metav1.ListOptions{ResourceVersion: agent.ResourceVersion}})
		stop()
	}
	if stream != nil {
		defer stream.Stop()
	}
	switch {
	case ctx.Err() != nil:
		reply(w, r, http.StatusGatewayTimeout, problem{msgTimeLimit})
		return
	case apierrors.IsNotFound(err):
		reply(w, r, http.StatusNotFound, problem{msgNoRun})
		return
	case err != nil:
		reply(w, r, http.StatusBadGateway, problem{"The watch is not available."})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if stream == nil {
		return
	}
	sendEvents(watchCtx, w, http.NewResponseController(w), stream, agent, a.recheck,
		func(ctx context.Context) string { return a.checkAccess(ctx, r, ref) })
}

// sendEvents writes the events of stream. Every recheck interval, it calls check, and it ends the
// watch if check gives a reason.
func sendEvents(ctx context.Context, w io.Writer, rc *http.ResponseController, stream watch.Interface,
	agent *corev1beta1.Agent, recheck time.Duration, check func(context.Context) string) {
	write := func(text string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(watchWriteTime))
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	var last []byte
	send := func(name string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if name == eventStatus && bytes.Equal(b, last) {
			return true
		}
		last = b
		return write("event: " + name + "\ndata: " + string(b) + "\n\n")
	}
	sendEnd := func(reason string) { send("end", map[string]string{"reason": reason}) }
	ping := time.NewTicker(watchPing)
	defer ping.Stop()
	access := time.NewTicker(recheck)
	defer access.Stop()
	if !write("retry: "+watchRetry+"\n\n") || !send(eventStatus, statusOf(agent)) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			// If the client closed the connection, there is no client to tell.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				sendEnd("timeLimit")
			}
			return
		case <-ping.C:
			if !write(": ping\n\n") {
				return
			}
		case <-access.C:
			// If the watch time ends during the check, the next loop sends the reason timeLimit.
			if reason := check(ctx); reason != "" && ctx.Err() == nil {
				sendEnd(reason)
				return
			}
		case event, ok := <-stream.ResultChan():
			next, isAgent := event.Object.(*corev1beta1.Agent)
			switch {
			case !ok || event.Type == watch.Error:
				sendEnd("watchEnded")
				return
			case !isAgent || next.Name != agent.Name || next.Namespace != agent.Namespace:
			case event.Type == watch.Deleted:
				send("deleted", statusOf(next))
				return
			case !send(eventStatus, statusOf(next)):
				return
			}
		}
	}
}
