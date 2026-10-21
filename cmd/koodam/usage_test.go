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
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/usage"
)

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

func setupUsage(t *testing.T, ws *corev1alpha1.Workspace) (*[][]string, func(...string) (string, error)) {
	t.Helper()
	_, _ = sshHome(t)
	kube := newKube(t, nil, ws)
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	calls := fakeUsage(t, remoteUsage, nil)
	return calls, func(args ...string) (string, error) { return runSSH(kube, "", args...) }
}

func TestUsageShowsTheProjectedCost(t *testing.T) {
	calls, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	out, err := run("usage", wsName)
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
	wantAll(t, lines[3], "total", "1030", "205", projected(cost), "no price for 1 of the models", "source: subscription")
	wantAll(t, lines[4], tab.Commit[:12])
	if strings.Contains(out, "gateway") {
		t.Errorf("a Workspace with no gateway key must have no gateway note:\n%s", out)
	}
}

func projected(c float64) string { return fmt.Sprintf("projected %.4f USD", c) }

func TestUsageNotesTheGatewayKey(t *testing.T) {
	ws := testWorkspace(hostKey, corev1alpha1.WorkspaceRunning)
	ws.Spec.Gateway = &corev1alpha1.WorkspaceGateway{Model: "m", BudgetPerDay: resource.MustParse("1")}
	_, run := setupUsage(t, ws)
	out, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	wantAll(t, out, usageGatewayNote, "projected", "source: subscription")
}

func TestUsageWithNoSessionFiles(t *testing.T) {
	_, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	fakeUsage(t, "", nil)
	out, err := run("usage", wsName)
	if err != nil {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	wantAll(t, out, "No token counts")
}

func TestUsageErrors(t *testing.T) {
	_, run := setupUsage(t, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	fakeUsage(t, "", errors.New("exit status 255"))
	if _, err := run("usage", wsName); err == nil || !strings.Contains(err.Error(), "koodam ssh test -n "+testNS) {
		t.Errorf("ssh failure: got %v", err)
	}
	fakeUsage(t, "claude FIXTURE-TEXT\n", nil)
	if _, err := run("usage", wsName); err == nil || strings.Contains(err.Error(), "FIXTURE") {
		t.Errorf("bad output: got %v", err)
	}
	if _, err := run("usage", "other"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing Workspace: got %v", err)
	}
}
