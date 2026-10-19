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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	testNS   = "dev"
	testName = "fix-tests"
	// fakeLog is the log body of each pod in the fake clientset.
	fakeLog = "fake logs"
	testJob = "kd-fix-tests"
	// cmdLogs and cmdRun are command names.
	cmdLogs = "logs"
	cmdRun  = "run"
	// flagWait and flagTimeout are the flags of the waits.
	flagWait    = "--wait"
	flagTimeout = "--timeout"
	agentUID    = types.UID("agent-uid")
	jobUID      = types.UID("job-uid")
)

const manifest = `apiVersion: core.koodam.dev/v1alpha1
kind: Agent
metadata:
  name: fix-tests
spec:
  image: example/agent:1
  command: ["sh", "run.sh"]
  model: local-agent
  budget: "2"
`

func testAgent(phase corev1alpha1.AgentPhase, usage *corev1alpha1.AgentUsage) *corev1alpha1.Agent {
	return &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS, UID: agentUID},
		Spec: corev1alpha1.AgentSpec{Image: "example/agent:1", Command: []string{"sh"}, Model: "local-agent",
			Budget: resource.MustParse("2")},
		Status: corev1alpha1.AgentStatus{Phase: phase, Job: testJob, KeyAlias: "koodam-dev-fix-tests", Usage: usage},
	}
}

// owner gives a controller reference to o with the UID uid.
func owner(kind, name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid, Controller: new(true)}}
}

// testJobObject is the Agent's Job. The Agent controls it.
func testJobObject() *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: testJob, Namespace: testNS, UID: jobUID,
		OwnerReferences: owner("Agent", testName, agentUID)}}
}

// testPod is a started pod of the Agent's Job.
func testPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "kd-fix-tests-abcde", Namespace: testNS,
			Labels: map[string]string{agentLabel: testName}, OwnerReferences: owner("Job", testJob, jobUID)},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// run runs the koodam command with args against the fake client kube.
func run(t *testing.T, kube client.WithWatch, args ...string) (string, error) {
	t.Helper()
	return runWith(t, kube, kubefake.NewClientset(), args...)
}

// runWith is run with the fake clientset cs for the pod log.
func runWith(t *testing.T, kube client.WithWatch, cs *kubefake.Clientset, args ...string) (string, error) {
	t.Helper()
	e := &env{kube: kube, clientset: cs, namespace: testNS, poll: time.Millisecond}
	root := newRoot(func() (*env, error) { return e, nil })
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	root.SetContext(context.Background())
	err := root.Execute()
	return out.String(), err
}

func newKube(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantAll(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("output does not contain %q:\n%s", p, out)
		}
	}
}

func TestRunCreatesTheAgent(t *testing.T) {
	kube := newKube(t, nil)
	out, err := run(t, kube, cmdRun, "-f", writeManifest(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "agent/fix-tests created")
	a := &corev1alpha1.Agent{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: testName}, a); err != nil {
		t.Fatalf("the Agent is not in the namespace of the flags: %v", err)
	}
}

func TestRunRefusesANamespaceThatDoesNotAgreeWithTheFlag(t *testing.T) {
	body := strings.Replace(manifest, "name: fix-tests", "name: fix-tests\n  namespace: prod", 1)
	_, err := run(t, newKube(t, nil), "-n", testNS, cmdRun, "-f", writeManifest(t, body))
	if err == nil || !strings.Contains(err.Error(), "does not agree") {
		t.Fatalf("run did not refuse the namespace: %v", err)
	}
}

func TestRunRefusesAnAgentThatExists(t *testing.T) {
	kube := newKube(t, nil, testAgent(corev1alpha1.AgentSucceeded, nil))
	_, err := run(t, kube, cmdRun, "-f", writeManifest(t, manifest))
	if err == nil || !strings.Contains(err.Error(), "agent/fix-tests exists") {
		t.Fatalf("run did not refuse an Agent that exists: %v", err)
	}
}

func TestRunDoesNotShowValuesFromAnInvalidFile(t *testing.T) {
	const marker = "MARKER-7f3a"
	for _, body := range []string{
		strings.Replace(manifest, `budget: "2"`, "budget: "+marker, 1),
		strings.Replace(manifest, "model: local-agent", "model: [\""+marker+"\"", 1),
		strings.Replace(manifest, "image: example/agent:1", "image: {"+marker+": 1}", 1),
		// The strict decode puts an unknown key in the error.
		strings.Replace(manifest, "image: example/agent:1", "image: example/agent:1\n  "+marker+": x", 1),
	} {
		_, err := run(t, newKube(t, nil), cmdRun, "-f", writeManifest(t, body))
		if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), "not a valid") {
			t.Errorf("the error is not sanitized: %v", err)
		}
	}
}

// marker is a harmless value that a test puts in each part of an API server error.
const marker = "MARKER-user:password"

func markerErrors() []error {
	gk := corev1alpha1.GroupVersion.WithKind("Agent").GroupKind()
	return []error{
		// The marker is in the name, the field path, the value and the detail.
		apierrors.NewInvalid(gk, marker, field.ErrorList{
			field.Invalid(field.NewPath("metadata", "labels").Key(marker), marker, "bad "+marker)}),
		// An admission webhook can echo stored values in the message.
		&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 400,
			Reason: metav1.StatusReasonBadRequest, Message: "admission webhook denied " + marker,
			Details: &metav1.StatusDetails{Name: marker, Kind: "Agent",
				Causes: []metav1.StatusCause{{Field: marker, Message: marker}}}}},
	}
}

func TestAPIErrorsDoNotShowValues(t *testing.T) {
	for _, apiErr := range markerErrors() {
		fail := func() error { return apiErr }
		funcs := &interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return fail()
			},
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return fail()
			},
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return fail()
			},
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return fail()
			},
		}
		for _, args := range [][]string{
			{cmdRun, "-f", writeManifest(t, manifest)}, {"delete", testName}, {"get", testName}, {"spend"},
			{cmdLogs, testName},
		} {
			_, err := run(t, newKube(t, funcs), args...)
			if err == nil {
				t.Fatalf("%v did not return the API error", args)
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("%v shows a value from the API error: %v", args, err)
			}
			if !strings.Contains(err.Error(), string(apierrors.ReasonForError(apiErr))) {
				t.Errorf("%v does not show the reason: %v", args, err)
			}
		}
	}
}

func TestRunRefusesAnotherKind(t *testing.T) {
	body := strings.Replace(manifest, "kind: Agent", "kind: Pod", 1)
	if _, err := run(t, newKube(t, nil), cmdRun, "-f", writeManifest(t, body)); err == nil {
		t.Fatal("run accepted a manifest that is not an Agent")
	}
}

// runEnds gives an interceptor that does the work of the controller: on create, the run ends
// and the usage is written.
func runEnds() *interceptor.Funcs {
	return &interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object,
		opts ...client.CreateOption) error {
		if a, ok := o.(*corev1alpha1.Agent); ok {
			a.UID, a.Status.Job = agentUID, testJob
			a.Status.Phase = corev1alpha1.AgentSucceeded
			a.Status.Usage = &corev1alpha1.AgentUsage{Spend: "0.25", Calls: 3, InputTokens: 10, OutputTokens: 20}
			a.Status.Conditions = []metav1.Condition{{Type: condRevoked, Status: metav1.ConditionTrue,
				Reason: "JobComplete", Message: "The spend did not settle, so the usage can be low.",
				LastTransitionTime: metav1.Now()}}
		}
		return c.Create(ctx, o, opts...)
	}}
}

func TestRunWaitShowsTheLogAndTheSpend(t *testing.T) {
	funcs := runEnds()
	out, err := run(t, newKube(t, funcs, testJobObject(), testPod()), cmdRun, "-f", writeManifest(t, manifest), flagWait)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, fakeLog, "agent/fix-tests Succeeded", "spend 0.25 of 2, 3 calls",
		"revoke: The spend did not settle")

	// A log error is a warning without values, and the spend still shows.
	cs := kubefake.NewClientset()
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return action.GetSubresource() == "log", nil, markerErrors()[1]
	})
	kube := newKube(t, funcs, testJobObject(), testPod())
	out, err = runWith(t, kube, cs, cmdRun, "-f", writeManifest(t, manifest), flagWait)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "warning: read the pod log: BadRequest", "spend 0.25 of 2, 3 calls")
	if strings.Contains(out, marker) || strings.Contains(out, testPod().Name) {
		t.Errorf("the log warning shows a value or the pod name:\n%s", out)
	}
}

func TestGetAndSpendShowThePhaseTheBudgetAndTheUsage(t *testing.T) {
	a := testAgent(corev1alpha1.AgentSucceeded, &corev1alpha1.AgentUsage{Spend: "0.5", Calls: 4, FailedCalls: 1})
	a.Status.StartedAt = &metav1.Time{Time: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	a.Status.CompletedAt = &metav1.Time{Time: time.Date(2026, 10, 18, 9, 4, 30, 0, time.UTC)}
	kube := newKube(t, nil, a)
	out, err := run(t, kube, "get")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "PHASE", "STARTED", "COMPLETED", testName, "Succeeded", "local-agent", "0.5",
		"2026-10-18T09:00:00Z", "2026-10-18T09:04:30Z", "koodam-dev-fix-tests")

	out, err = run(t, kube, "spend", testName)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "BUDGET", "FAILED", testName, "Succeeded", "0.5")
	if _, err := run(t, kube, "get", "missing"); err == nil {
		t.Error("get did not fail for a missing Agent")
	}
}

func TestGetShowsADashForATimeThatIsNotSet(t *testing.T) {
	agent := testAgent(corev1alpha1.AgentRunning, nil)
	started := metav1.NewTime(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC))
	agent.Status.StartedAt = &started
	out, err := run(t, newKube(t, nil, agent), "get", testName)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "2026-10-18T09:00:00Z")
	if got := strings.Count(out, " - "); got != 2 {
		t.Errorf("get shows %d dashes, want a dash for the spend and for the end time:\n%s", got, out)
	}
}

func TestLogsShowsThePodLog(t *testing.T) {
	out, err := run(t, newKube(t, nil, testAgent(corev1alpha1.AgentRunning, nil), testJobObject(), testPod()),
		cmdLogs, testName)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, fakeLog)
	if _, err := run(t, newKube(t, nil), cmdLogs, testName); err == nil {
		t.Error("logs did not fail for an Agent without a pod")
	}
}

func TestPodIsFromTheCurrentJobOnly(t *testing.T) {
	// The other pod is newer and has the Agent label, but another Job controls it.
	other := testPod()
	other.Name, other.OwnerReferences = "copied-label", owner("Job", testJob, "old-job-uid")
	other.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Hour))
	a := testAgent(corev1alpha1.AgentRunning, nil)
	e := &env{kube: newKube(t, nil, a, testJobObject(), testPod(), other), namespace: testNS}
	pod, err := e.pod(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Name != testPod().Name {
		t.Errorf("pod gave %s, not the pod of the current Job", pod.Name)
	}
}

func TestLogsFollowWaitsForThePod(t *testing.T) {
	// The first pod lists are empty, as before the Job makes the pod.
	lists := 0
	funcs := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := l.(*corev1.PodList); ok && lists < 3 {
			lists++
			return nil
		}
		return c.List(ctx, l, opts...)
	}}
	kube := newKube(t, funcs, testAgent(corev1alpha1.AgentRunning, nil), testJobObject(), testPod())
	out, err := run(t, kube, cmdLogs, testName, "-f")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, fakeLog)
	if lists != 3 {
		t.Errorf("logs -f did not wait for the pod: %d empty lists", lists)
	}
}

func TestTheLogStreamHasNoDeadlineFromTheTimeout(t *testing.T) {
	funcs := runEnds()
	for _, args := range [][]string{
		{cmdLogs, testName, "-f", flagTimeout, "1m"},
		{cmdRun, "-f", writeManifest(t, manifest), flagWait, flagTimeout, "1m"},
	} {
		objs := []client.Object{testJobObject(), testPod()}
		if args[0] == cmdLogs {
			objs = append(objs, testAgent(corev1alpha1.AgentRunning, nil))
		}
		streams := 0
		e := &env{kube: newKube(t, funcs, objs...), clientset: kubefake.NewClientset(), namespace: testNS,
			poll: time.Millisecond,
			stream: func(ctx context.Context, _, _ string, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
				streams++
				if _, ok := ctx.Deadline(); ok {
					t.Errorf("%v: the log stream has a deadline", args)
				}
				return io.NopCloser(strings.NewReader(fakeLog)), nil
			}}
		root := newRoot(func() (*env, error) { return e, nil })
		root.SetOut(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if streams != 1 {
			t.Errorf("%v: %d log streams, not 1", args, streams)
		}
	}
}

func TestAStalledLogOpenStopsAtTheTimeout(t *testing.T) {
	// The fake open blocks until its context ends, as a stalled HTTP request does.
	stalled := func(ctx context.Context, _, _ string, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	for _, args := range [][]string{
		{cmdLogs, testName, "-f", flagTimeout, "50ms"},
		{cmdRun, "-f", writeManifest(t, manifest), flagWait, flagTimeout, "50ms"},
	} {
		objs := []client.Object{testJobObject(), testPod()}
		if args[0] == cmdLogs {
			objs = append(objs, testAgent(corev1alpha1.AgentRunning, nil))
		}
		e := &env{kube: newKube(t, runEnds(), objs...), clientset: kubefake.NewClientset(), namespace: testNS,
			poll: time.Millisecond, stream: stalled}
		root := newRoot(func() (*env, error) { return e, nil })
		out := &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(out)
		root.SetArgs(args)
		start := time.Now()
		err := root.ExecuteContext(context.Background())
		if time.Since(start) > 5*time.Second {
			t.Errorf("%v did not stop at the timeout", args)
		}
		if args[0] == cmdLogs {
			if err == nil {
				t.Errorf("logs -f did not fail on a stalled open")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		wantAll(t, out.String(), "warning: read the pod log", "spend 0.25 of 2, 3 calls")
	}
}

func TestDeleteWaitsUntilTheAgentIsGone(t *testing.T) {
	kube := newKube(t, nil, testAgent(corev1alpha1.AgentRunning, nil))
	out, err := run(t, kube, "delete", testName, flagWait)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "agent/fix-tests deleted", "revoked and gone")
}

func TestDeleteWaitStopsAtTheTimeout(t *testing.T) {
	// The finalizer stays because no controller runs, so the Agent does not go.
	a := testAgent(corev1alpha1.AgentRunning, nil)
	a.Finalizers = []string{"koodam.dev/revoke"}
	_, err := run(t, newKube(t, nil, a), "delete", testName, flagWait, flagTimeout, "20ms")
	if err == nil {
		t.Fatal("delete --wait did not stop at the timeout")
	}
}
