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
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	kubefake "k8s.io/client-go/kubernetes/fake"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/usage"
)

// usageContext is the kube context of the usage tests.
const usageContext = "kind-test"

// remoteUsage is an output of usage.RemoteScript with 1 priced and 1 unknown model.
const remoteUsage = `claude "koodam_line":1
claude "model":"claude-haiku-4-5"
claude "id":"msg_a"
claude "input_tokens":1000
claude "output_tokens":100
claude "koodam_line":1
claude "model":"claude-haiku-4-5"
claude "id":"msg_a"
claude "input_tokens":1000
claude "output_tokens":200
codex "koodam_line":1
codex "model":"fixture-unknown-model"
codex "input_tokens":50
codex "cached_input_tokens":20
codex "output_tokens":5
`

// fakeUsage replaces outputProgram. It records each call and gives out and err.
func fakeUsage(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	old := outputProgram
	t.Cleanup(func() { outputProgram = old })
	outputProgram = func(_ *cobra.Command, in, name string, args ...string) ([]byte, error) {
		if in != usage.RemoteScript {
			t.Errorf("the stdin of ssh is not the fixed script")
		}
		calls = append(calls, append([]string{name}, args...))
		return []byte(out), err
	}
	return &calls
}

// setupUsage makes the SSH entry for ws and gives a run function with a separate stdout and
// stderr.
func setupUsage(t *testing.T, ws *corev1alpha1.Workspace) (*[][]string, func(...string) (string, string, error)) {
	t.Helper()
	_, _ = sshHome(t)
	kube := newKube(t, nil, ws)
	// The env namespace is the namespace that -n gives in kubeEnv.
	if out, err := runSSHEnv(&env{kube: kube, namespace: ws.Namespace, context: usageContext}, "", "ssh", "setup",
		ws.Name, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	calls := fakeUsage(t, remoteUsage, nil)
	return calls, func(args ...string) (string, string, error) {
		cs := kubefake.NewClientset()
		allow(cs, true)
		e := &env{kube: kube, clientset: cs, namespace: ws.Namespace, context: usageContext}
		root := newRoot(func() (*env, error) { return e, nil })
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(errOut)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(args)
		root.SetContext(context.Background())
		err := root.Execute()
		return out.String(), errOut.String(), err
	}
}

func TestUsageShowsTheProjectedCost(t *testing.T) {
	calls, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	out, errOut, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	want := []string{sshProgram, "-T", wsAlias, "sh", "-s"}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("calls: got %q, want %q", *calls, want)
	}
	tab, err := usage.Prices()
	if err != nil {
		t.Fatal(err)
	}
	cost, ok := tab.Cost(usage.Row{Model: "claude-haiku-4-5", Input: 1000, Output: 200})
	if !ok {
		t.Fatal("no price for claude-haiku-4-5")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	wantAll(t, lines[1], "claude-code", "claude-haiku-4-5", "1000", "200", projected(cost), "source: subscription")
	wantAll(t, lines[2], "codex", "fixture-unknown-model", "30", "5", "20", "no price", "source: subscription")
	wantAll(t, lines[3], "total", "1030", "205", projected(cost), "no price for 1 of the models: fixture-unknown-model",
		"source: subscription")
	wantAll(t, lines[4], tab.Commit[:12])
	if errOut != "" {
		t.Errorf("a Workspace with no gateway key must have no note:\n%s", errOut)
	}
}

func projected(c float64) string { return fmt.Sprintf("projected %.4f USD", c) }

func TestUsageUsesTheNamespace(t *testing.T) {
	ws := testWorkspace(hostKey, corev1alpha1.WorkspaceRunning)
	ws.Namespace = "team"
	calls, run := setupUsage(t, ws)
	if out, _, err := run("usage", wsName); err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	want := []string{sshProgram, "-T", hostAlias("team", wsName), "sh", "-s"}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("calls: got %q, want %q", *calls, want)
	}
	fakeUsage(t, "", errors.New("exit status 255"))
	if _, _, err := run("usage", wsName); err == nil || !strings.Contains(err.Error(), "koodam ssh test -n team "+wsName) {
		t.Errorf("ssh failure: got %v", err)
	}
}

func TestUsageNotesTheGatewayKeyOnStderr(t *testing.T) {
	ws := testWorkspace(hostKey, corev1alpha1.WorkspaceRunning)
	ws.Spec.Gateway = &corev1alpha1.WorkspaceGateway{Model: "m", BudgetPerDay: resource.MustParse("1")}
	_, run := setupUsage(t, ws)
	out, errOut, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	wantAll(t, errOut, "gateway spend is the real spend", "kubectl get workspace "+wsName+" -n "+testNS,
		"{.status.usage}")
	wantAll(t, out, "projected", "source: subscription")
	if strings.Contains(out, "gateway") {
		t.Errorf("the note must not be on stdout:\n%s", out)
	}
}

func TestUsageWhenNoModelHasAPrice(t *testing.T) {
	_, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	fakeUsage(t, "codex \"koodam_line\":1\ncodex \"model\":\"fixture-a\"\ncodex \"input_tokens\":5\n"+
		"codex \"koodam_line\":1\ncodex \"model\":\"fixture-a\"\ncodex \"input_tokens\":6\n", nil)
	out, _, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	if strings.Contains(out, "projected") {
		t.Errorf("no cost must show when no model has a price:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	wantAll(t, lines[len(lines)-2], "total", "11", "no price", "source: subscription")
}

func TestUsageWithNoSessionFiles(t *testing.T) {
	_, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	fakeUsage(t, "", nil)
	out, _, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	wantAll(t, out, "No token counts")
}

func TestUsageErrors(t *testing.T) {
	_, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	fakeUsage(t, "", errors.New("exit status 255"))
	if _, _, err := run("usage", wsName); err == nil || !strings.Contains(err.Error(), "koodam ssh test -n "+testNS) {
		t.Errorf("ssh failure: got %v", err)
	}
	fakeUsage(t, "claude FIXTURE-TEXT\n", nil)
	if _, _, err := run("usage", wsName); err == nil || strings.Contains(err.Error(), "FIXTURE") {
		t.Errorf("bad output: got %v", err)
	}
	if _, _, err := run("usage", "other"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing Workspace: got %v", err)
	}
}

func TestUsageRefusesAWorkspaceThatIsNotRunning(t *testing.T) {
	calls, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceSuspended))
	if _, _, err := run("usage", wsName); err == nil || !strings.Contains(err.Error(), "not Running") {
		t.Errorf("got %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("koodam must not connect: got %q", *calls)
	}
}

func TestUsageNeedsTheSSHEntry(t *testing.T) {
	_, _ = sshHome(t)
	fakeUsage(t, remoteUsage, nil)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	_, err := runSSH(kube, "", "usage", wsName)
	if err == nil || !strings.Contains(err.Error(), "koodam ssh setup "+wsName) {
		t.Errorf("got %v", err)
	}
}

// The real outputProgram must drop the stderr of the remote command.
func TestOutputProgramDropsTheStderr(t *testing.T) {
	cmd := &cobra.Command{}
	errOut := &bytes.Buffer{}
	cmd.SetErr(errOut)
	cmd.SetContext(context.Background())
	out, err := outputProgram(cmd, "in", "sh", "-c", "cat; echo FIXTURE-REMOTE-ERROR >&2")
	if err != nil || string(out) != "in" || errOut.Len() != 0 {
		t.Errorf("got out %q, stderr %q, error %v", out, errOut.String(), err)
	}
}
