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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/docs"
)

const (
	runName = "report-7"
	jobName = "kd-report-7"
	podName = "kd-report-7-abcde"
	// agentContainer is the name of the container of the agent.
	agentContainer = "agent"
	testPath       = runPrefix + testNamespace + "/" + runName
)

var runTime = metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// runObjects gives an Agent, its Job, the pod of the Job and a pod with the same labels that the
// Job does not control.
func runObjects(hash string) []client.Object {
	agent := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: testNamespace, UID: "uid-agent"},
		Status: corev1alpha1.AgentStatus{
			RunID: "run-7", Phase: corev1alpha1.AgentRunning, Job: jobName, KeyHash: hash, StartedAt: &runTime,
			Conditions: []metav1.Condition{{Type: "Provisioned", Status: metav1.ConditionTrue, Reason: "KeyReady",
				Message: "The key is ready.", LastTransitionTime: runTime}},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: testNamespace, UID: "uid-job",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: corev1alpha1.GroupVersion.String(), Kind: "Agent",
				Name: runName, UID: agent.UID, Controller: new(true)}}},
		Status: batchv1.JobStatus{Active: 1, StartTime: &runTime, Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionFalse, LastTransitionTime: runTime}}},
	}
	labels := map[string]string{batchv1.JobNameLabel: jobName}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: testNamespace, Labels: labels, CreationTimestamp: runTime,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobName,
				UID: job.UID, Controller: new(true)}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, StartTime: &runTime,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "install", State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", Message: hash}}}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: agentContainer, Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: runTime}}}},
		},
	}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kd-report-7-other", Namespace: testNamespace, Labels: labels}}
	return []client.Object{agent, job, pod, other}
}

func runClient(t testing.TB, objects ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1alpha1.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}

func TestRunDetail(t *testing.T) {
	f := newFixture(t, 0)
	f.kube = runClient(t, runObjects(f.runKeys[0].hash)...)
	d := decode[runDetail](t, f.call(t, http.MethodGet, testPath, f.token, http.StatusOK))
	if d.ID != "run-7" || d.Name != runName || d.Phase != "Running" || len(d.Conditions) != 1 ||
		d.Conditions[0].Reason != "KeyReady" || d.StartedAt == nil || !d.StartedAt.Equal(runTime.Time) {
		t.Fatalf("the run fields do not match: %+v", d)
	}
	if d.Job == nil || d.Job.Name != jobName || d.Job.Active != 1 || len(d.Job.Conditions) != 1 {
		t.Fatalf("the Job fields do not match: %+v", d.Job)
	}
	if len(d.Pods) != 1 || d.Pods[0].Name != podName || d.Pods[0].Phase != "Running" || len(d.Pods[0].Containers) != 2 {
		t.Fatalf("want only the pod that the Job controls: %+v", d.Pods)
	}
	install, agent := d.Pods[0].Containers[0], d.Pods[0].Containers[1]
	if !install.Init || install.State != "terminated" || install.ExitCode == nil || *install.ExitCode != 0 ||
		agent.Init || agent.State != "running" || !agent.Ready {
		t.Fatalf("the container states do not match: %+v", d.Pods[0].Containers)
	}
	f.call(t, http.MethodHead, testPath, f.token, http.StatusOK)
	f.call(t, http.MethodGet, testPath, "", http.StatusUnauthorized)
	f.call(t, http.MethodGet, testPath+"?pod=x", f.token, http.StatusBadRequest)
	f.call(t, http.MethodGet, runPrefix+testNamespace+"/missing", f.token, http.StatusNotFound)
	f.call(t, http.MethodGet, runPrefix+otherNamespace+"/"+runName, f.token, http.StatusForbidden)
	for _, path := range []string{runPrefix + testNamespace, testPath + "/a/b", testPath + "/next", testPath + "/",
		runPrefix + "Bad/" + runName, runPrefix + testNamespace + "/a_b"} {
		f.call(t, http.MethodGet, path, f.token, http.StatusNotFound)
	}
}

func TestRunWithNoJob(t *testing.T) {
	f := newFixture(t, 0)
	objects := runObjects(f.runKeys[0].hash)
	job := objects[1].(*batchv1.Job)
	job.OwnerReferences[0].UID = types.UID("uid-of-an-older-agent")
	f.kube = runClient(t, objects...)
	d := decode[runDetail](t, f.call(t, http.MethodGet, testPath, f.token, http.StatusOK))
	if d.Job != nil || len(d.Pods) != 0 {
		t.Fatal("a Job that the Agent does not control is not part of the run")
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(f.call(t, http.MethodGet, testPath, f.token, http.StatusOK).Body.Bytes(), &body) != nil ||
		string(body["pods"]) != "[]" || string(body["conditions"]) == "null" {
		t.Fatal("want empty arrays, not null")
	}
	f.kube = runClient(t, objects[0])
	if d := decode[runDetail](t, f.call(t, http.MethodGet, testPath, f.token, http.StatusOK)); d.Job != nil {
		t.Fatal("want no Job when the Job is gone")
	}
}

type failingGet struct {
	client.Reader
	err error
}

func (r failingGet) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestRunKubernetesErrors(t *testing.T) {
	f := newFixture(t, 0)
	f.kube = failingGet{err: errors.New("the API server is not available")}
	f.call(t, http.MethodGet, testPath, f.token, http.StatusBadGateway)
	f.kube = failingGet{err: context.DeadlineExceeded}
	f.timeout = time.Nanosecond
	f.call(t, http.MethodGet, testPath, f.token, http.StatusGatewayTimeout)
}

func TestViewerReadsOneRun(t *testing.T) {
	f := newViewerFixture(t)
	f.kube = runClient(t, runObjects(f.runKeys[0].hash)...)
	f.call(t, http.MethodGet, testPath, f.reader, http.StatusOK)
	if len(f.reviews) != 1 || f.reviews[0].ResourceAttributes.Namespace != testNamespace ||
		f.reviews[0].ResourceAttributes.Verb != "list" || f.reviews[0].ResourceAttributes.Resource != "agents" {
		t.Fatalf("want one review of list agents in the run namespace: %+v", f.reviews)
	}
	f.call(t, http.MethodGet, runPrefix+otherNamespace+"/"+runName, f.reader, http.StatusForbidden)
	f.call(t, http.MethodGet, runPrefix+"unknown/"+runName, f.reader, http.StatusForbidden)
	f.call(t, http.MethodGet, testPath, f.stranger, http.StatusForbidden)
	f.call(t, http.MethodGet, testPath, "not-a-token", http.StatusUnauthorized)
}

func TestRunPathInOpenAPI(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(docs.SpendOpenAPI), &spec); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 0)
	for path, concrete := range map[string]string{
		"/v1/runs/{namespace}/{name}": testPath, "/v1/runs/{namespace}/{name}/log": testPath + "/log",
	} {
		for _, method := range []string{"get", "head", "post", "put", "patch", "delete", "options"} {
			if _, ok := spec.Paths[path][method]; !ok {
				t.Fatalf("the OpenAPI document does not list %s of %s", method, path)
			}
		}
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			f.call(t, method, concrete, f.token, http.StatusMethodNotAllowed)
		}
	}
	if !slices.ContainsFunc(strings.Split(docs.SpendOpenAPI, "\n"), func(l string) bool {
		return strings.Contains(l, `"RunDetail"`) || strings.Contains(l, `"RunLog"`)
	}) {
		t.Fatal("the OpenAPI document has no RunDetail schema")
	}
}

// pagedPods gives the pods in pages of the list limit, in name order, as the API server can.
type pagedPods struct {
	client.Reader
}

func (p pagedPods) List(ctx context.Context, target client.ObjectList, options ...client.ListOption) error {
	pods, ok := target.(*corev1.PodList)
	if !ok {
		return p.Reader.List(ctx, target, options...)
	}
	o := (&client.ListOptions{}).ApplyOptions(options)
	if o.Limit < 1 || o.Limit > maxPage {
		return errors.New("the pod list needs a limit")
	}
	all := &corev1.PodList{}
	if err := p.Reader.List(ctx, all, client.InNamespace(o.Namespace), client.MatchingLabelsSelector{Selector: o.LabelSelector}); err != nil {
		return err
	}
	slices.SortFunc(all.Items, func(x, y corev1.Pod) int { return strings.Compare(x.Name, y.Name) })
	start, _ := strconv.Atoi(o.Continue)
	end := min(start+int(o.Limit), len(all.Items))
	pods.Items = all.Items[start:end]
	if end < len(all.Items) {
		pods.Continue = strconv.Itoa(end)
	}
	return nil
}

// TestRunPodsReadsEachPage puts the pod of the run after more than one page of other pods with the
// same label: pods of an older Job, and pods of this Job that are older.
func TestRunPodsReadsEachPage(t *testing.T) {
	f := newFixture(t, 0)
	objects := runObjects(f.runKeys[0].hash)
	for i := range 2 * maxPage {
		pod := objects[2].DeepCopyObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("kd-report-7-a%04d", i)
		pod.CreationTimestamp = metav1.NewTime(runTime.Add(-time.Hour))
		if i%2 == 0 {
			pod.OwnerReferences[0].UID = "uid-of-an-older-job"
		}
		objects = append(objects, pod)
	}
	f.kube = pagedPods{runClient(t, objects...)}
	d := decode[runDetail](t, f.call(t, http.MethodGet, testPath, f.token, http.StatusOK))
	if len(d.Pods) != maxPods || d.Pods[0].Name != podName {
		t.Fatalf("want the newest %d pods of the Job, with the run pod first: %d pods", maxPods, len(d.Pods))
	}
	for i := range maxPodScan {
		pod := objects[2].DeepCopyObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("kd-report-7-b%04d", i)
		objects = append(objects, pod)
	}
	f.kube = pagedPods{runClient(t, objects...)}
	f.call(t, http.MethodGet, testPath, f.token, http.StatusBadGateway)
}

func TestConditionMessagesAreRedacted(t *testing.T) {
	f := newFixture(t, 0)
	objects := runObjects(f.runKeys[0].hash)
	agent := objects[0].(*corev1alpha1.Agent)
	agent.Status.Conditions[0].Message = "the gateway refused the key " + f.runKeys[0].value
	job := objects[1].(*batchv1.Job)
	job.Status.Conditions[0].Message = "mount failed:\n-----BEGIN PRIVATE KEY-----\n" + keyBody + "\n-----END PRIVATE KEY-----\nend"
	f.kube = runClient(t, objects...)
	w := f.call(t, http.MethodGet, testPath, f.token, http.StatusOK)
	if strings.Contains(w.Body.String(), keyBody) {
		t.Fatal("the response has the body of a private key")
	}
	d := decode[runDetail](t, w)
	if d.Conditions[0].Message != "the gateway refused the key [redacted]" {
		t.Fatalf("the condition message is not redacted: %q", d.Conditions[0].Message)
	}
	if want := "mount failed:\n[redacted]\n[redacted]\n[redacted]\nend"; d.Job.Conditions[0].Message != want {
		t.Fatalf("the Job condition message is %q, want %q", d.Job.Conditions[0].Message, want)
	}
}
