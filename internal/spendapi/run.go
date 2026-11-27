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
	"net/http"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	runPrefix = runsPath + "/"
	// maxPods limits the pods of one run in a response. A run Job has no retries, so it has one pod.
	maxPods = 10
	// maxPodScan limits the pods with the Job label that one request reads.
	maxPodScan = 500
)

var errTooManyPods = errors.New("too many pods have the label of the Job")

// runRef is a path /v1/runs/{namespace}/{name}, with an optional last part.
type runRef struct {
	namespace, name, part string
}

// parseRun gives the run of a path under /v1/runs/. The second value is false for a path that is
// not valid. The names must be valid Kubernetes names, so they cannot hold a path or a query.
func parseRun(path string) (runRef, bool) {
	rest, found := strings.CutPrefix(path, runPrefix)
	parts := strings.Split(rest, "/")
	if !found || len(parts) < 2 || len(parts) > 3 || len(validation.IsDNS1123Label(parts[0])) != 0 ||
		len(validation.IsDNS1123Subdomain(parts[1])) != 0 {
		return runRef{}, false
	}
	ref := runRef{namespace: parts[0], name: parts[1]}
	if len(parts) == 3 {
		if parts[2] == "" {
			return runRef{}, false
		}
		ref.part = parts[2]
	}
	return ref, ref.part == "" || ref.part == logPart || ref.part == watchPart
}

type condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type runJob struct {
	Name        string      `json:"name"`
	Active      int32       `json:"active"`
	Succeeded   int32       `json:"succeeded"`
	Failed      int32       `json:"failed"`
	StartedAt   *time.Time  `json:"startedAt,omitempty"`
	CompletedAt *time.Time  `json:"completedAt,omitempty"`
	Conditions  []condition `json:"conditions"`
}

// runContainer has no message of the container state, because the termination message comes
// from the code of the run.
type runContainer struct {
	Name         string `json:"name"`
	Init         bool   `json:"init,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	ExitCode     *int32 `json:"exitCode,omitempty"`
}

type runPod struct {
	Name       string         `json:"name"`
	Phase      string         `json:"phase"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	Containers []runContainer `json:"containers"`
}

type runDetail struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Namespace   string      `json:"namespace"`
	Phase       string      `json:"phase,omitempty"`
	StartedAt   *time.Time  `json:"startedAt,omitempty"`
	CompletedAt *time.Time  `json:"completedAt,omitempty"`
	Conditions  []condition `json:"conditions"`
	Job         *runJob     `json:"job,omitempty"`
	Pods        []runPod    `json:"pods"`
}

func (a *api) serveRun(ctx context.Context, w http.ResponseWriter, r *http.Request, ref runRef) {
	if r.URL.RawQuery != "" {
		reply(w, r, http.StatusBadRequest, problem{msgNoQuery})
		return
	}
	result, err := a.runDetail(ctx, ref)
	switch {
	case ctx.Err() != nil:
		reply(w, r, http.StatusGatewayTimeout, problem{msgTimeLimit})
	case apierrors.IsNotFound(err):
		reply(w, r, http.StatusNotFound, problem{msgNoRun})
	case errors.Is(err, errTooManyPods):
		reply(w, r, http.StatusBadGateway, problem{"Too many pods have the label of the run Job."})
	case err != nil:
		reply(w, r, http.StatusBadGateway, problem{"The run data is not available."})
	default:
		reply(w, r, http.StatusOK, result)
	}
}

func (a *api) runDetail(ctx context.Context, ref runRef) (runDetail, error) {
	agent := &corev1alpha1.Agent{}
	if err := a.kube.Get(ctx, client.ObjectKey{Namespace: ref.namespace, Name: ref.name}, agent); err != nil {
		return runDetail{}, err
	}
	d := runDetail{
		ID: agent.Status.RunID, Name: agent.Name, Namespace: agent.Namespace, Phase: string(agent.Status.Phase),
		StartedAt: utc(agent.Status.StartedAt), CompletedAt: utc(agent.Status.CompletedAt),
		Conditions: conditions(agent.Status.Conditions), Pods: []runPod{},
	}
	if d.ID == "" {
		d.ID = string(agent.UID)
	}
	job, pods, err := a.runPods(ctx, agent)
	if err != nil || job == nil {
		return d, err
	}
	d.Job = &runJob{
		Name: job.Name, Active: job.Status.Active, Succeeded: job.Status.Succeeded, Failed: job.Status.Failed,
		StartedAt: utc(job.Status.StartTime), CompletedAt: utc(job.Status.CompletionTime),
		Conditions: make([]condition, 0, len(job.Status.Conditions)),
	}
	for _, c := range job.Status.Conditions {
		d.Job.Conditions = append(d.Job.Conditions, condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: redactText(c.Message),
			LastTransitionTime: c.LastTransitionTime.UTC(),
		})
	}
	for i := range pods {
		d.Pods = append(d.Pods, podOf(&pods[i]))
	}
	return d, nil
}

// runPods gives the Job of the Agent and its pods, newest first. A Job or a pod with the same name
// or labels that the run does not control is not part of the run, so the result does not have it.
func (a *api) runPods(ctx context.Context, agent *corev1alpha1.Agent) (*batchv1.Job, []corev1.Pod, error) {
	if agent.Status.Job == "" {
		return nil, nil, nil
	}
	job := &batchv1.Job{}
	err := a.kube.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: agent.Status.Job}, job)
	if apierrors.IsNotFound(err) || (err == nil && !metav1.IsControlledBy(job, agent)) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// The list order is not the creation order, and pods of an older Job can have the same label.
	// Thus the server reads each page, and it keeps the newest pods of this Job.
	var pods []corev1.Pod
	for scanned, next := 0, ""; ; {
		list := &corev1.PodList{}
		if err := a.kube.List(ctx, list, client.InNamespace(agent.Namespace),
			client.MatchingLabels{batchv1.JobNameLabel: job.Name}, client.Limit(maxPage), client.Continue(next)); err != nil {
			return nil, nil, err
		}
		scanned += len(list.Items)
		pods = append(pods, slices.DeleteFunc(list.Items, func(p corev1.Pod) bool { return !metav1.IsControlledBy(&p, job) })...)
		if next = list.Continue; next == "" {
			break
		}
		if scanned >= maxPodScan {
			return nil, nil, errTooManyPods
		}
	}
	slices.SortStableFunc(pods, func(x, y corev1.Pod) int {
		return y.CreationTimestamp.Compare(x.CreationTimestamp.Time)
	})
	return job, pods[:min(len(pods), maxPods)], nil
}

func conditions(in []metav1.Condition) []condition {
	out := make([]condition, 0, len(in))
	for _, c := range in {
		out = append(out, condition{
			Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: redactText(c.Message),
			LastTransitionTime: c.LastTransitionTime.UTC(),
		})
	}
	return out
}

func podOf(p *corev1.Pod) runPod {
	result := runPod{Name: p.Name, Phase: string(p.Status.Phase), StartedAt: utc(p.Status.StartTime)}
	result.Containers = make([]runContainer, 0, len(p.Status.InitContainerStatuses)+len(p.Status.ContainerStatuses))
	for i, statuses := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, s := range statuses {
			c := runContainer{Name: s.Name, Init: i == 0, Ready: s.Ready, RestartCount: s.RestartCount, State: "waiting"}
			switch {
			case s.State.Terminated != nil:
				c.State, c.Reason = "terminated", s.State.Terminated.Reason
				c.ExitCode = &s.State.Terminated.ExitCode
			case s.State.Running != nil:
				c.State = "running"
			case s.State.Waiting != nil:
				c.Reason = s.State.Waiting.Reason
			}
			result.Containers = append(result.Containers, c)
		}
	}
	return result
}
