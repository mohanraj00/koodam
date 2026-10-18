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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	testNS   = "dev"
	testName = "fix-tests"
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
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS},
		Spec: corev1alpha1.AgentSpec{Image: "example/agent:1", Command: []string{"sh"}, Model: "local-agent",
			Budget: resource.MustParse("2")},
		Status: corev1alpha1.AgentStatus{Phase: phase, KeyAlias: "koodam-dev-fix-tests", Usage: usage},
	}
}

// run runs the koodam command with args against the fake client kube.
func run(t *testing.T, kube client.WithWatch, args ...string) (string, error) {
	t.Helper()
	e := &env{kube: kube, namespace: testNS, poll: time.Millisecond}
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
	out, err := run(t, kube, "run", "-f", writeManifest(t, manifest))
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
	_, err := run(t, newKube(t, nil), "-n", testNS, "run", "-f", writeManifest(t, body))
	if err == nil || !strings.Contains(err.Error(), "does not agree") {
		t.Fatalf("run did not refuse the namespace: %v", err)
	}
}

func TestRunRefusesAnAgentThatExists(t *testing.T) {
	kube := newKube(t, nil, testAgent(corev1alpha1.AgentSucceeded, nil))
	_, err := run(t, kube, "run", "-f", writeManifest(t, manifest))
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
		_, err := run(t, newKube(t, nil), "run", "-f", writeManifest(t, body))
		if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), "not a valid") {
			t.Errorf("the error is not sanitized: %v", err)
		}
	}
}

func TestRunDoesNotShowValuesFromAnAPIError(t *testing.T) {
	const marker = "MARKER-user:password"
	funcs := &interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object,
		...client.CreateOption) error {
		return apierrors.NewInvalid(corev1alpha1.GroupVersion.WithKind("Agent").GroupKind(), testName,
			field.ErrorList{field.Invalid(field.NewPath("metadata", "labels"), marker, "bad "+marker)})
	}}
	_, err := run(t, newKube(t, funcs), "run", "-f", writeManifest(t, manifest))
	if err == nil {
		t.Fatal("run did not return the API error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("the error shows a value from the manifest: %v", err)
	}
	wantAll(t, err.Error(), "Invalid", testName, "metadata.labels")
}

func TestRunRefusesAnotherKind(t *testing.T) {
	body := strings.Replace(manifest, "kind: Agent", "kind: Pod", 1)
	if _, err := run(t, newKube(t, nil), "run", "-f", writeManifest(t, body)); err == nil {
		t.Fatal("run accepted a manifest that is not an Agent")
	}
}

func TestRunWaitShowsTheSpend(t *testing.T) {
	// The interceptor does the work of the controller: the run ends and the usage is written.
	funcs := &interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object,
		opts ...client.CreateOption) error {
		if a, ok := o.(*corev1alpha1.Agent); ok {
			a.Status.Phase = corev1alpha1.AgentSucceeded
			a.Status.Usage = &corev1alpha1.AgentUsage{Spend: "0.25", Calls: 3, InputTokens: 10, OutputTokens: 20}
			a.Status.Conditions = []metav1.Condition{{Type: condRevoked, Status: metav1.ConditionTrue,
				Reason: "JobComplete", Message: "The spend did not settle, so the usage can be low.",
				LastTransitionTime: metav1.Now()}}
		}
		return c.Create(ctx, o, opts...)
	}}
	out, err := run(t, newKube(t, funcs), "run", "-f", writeManifest(t, manifest), "--wait")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "agent/fix-tests Succeeded", "spend 0.25 of 2, 3 calls", "revoke: The spend did not settle")
}

func TestGetAndSpendShowThePhaseTheBudgetAndTheUsage(t *testing.T) {
	kube := newKube(t, nil, testAgent(corev1alpha1.AgentSucceeded,
		&corev1alpha1.AgentUsage{Spend: "0.5", Calls: 4, FailedCalls: 1}))
	out, err := run(t, kube, "get")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "PHASE", testName, "Succeeded", "local-agent", "0.5", "koodam-dev-fix-tests")

	out, err = run(t, kube, "spend", testName)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "BUDGET", "FAILED", testName, "Succeeded", "0.5")
	if _, err := run(t, kube, "get", "missing"); err == nil {
		t.Error("get did not fail for a missing Agent")
	}
}

func TestDeleteWaitsUntilTheAgentIsGone(t *testing.T) {
	kube := newKube(t, nil, testAgent(corev1alpha1.AgentRunning, nil))
	out, err := run(t, kube, "delete", testName, "--wait")
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "agent/fix-tests deleted", "revoked and gone")
}

func TestDeleteWaitStopsAtTheTimeout(t *testing.T) {
	// The finalizer stays because no controller runs, so the Agent does not go.
	a := testAgent(corev1alpha1.AgentRunning, nil)
	a.Finalizers = []string{"koodam.dev/revoke"}
	_, err := run(t, newKube(t, nil, a), "delete", testName, "--wait", "--timeout", "20ms")
	if err == nil {
		t.Fatal("delete --wait did not stop at the timeout")
	}
}
