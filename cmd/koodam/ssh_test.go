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
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	userKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIUserKey"
	otherKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKey other@host"
	hostKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey"
	wsName    = "main"
	wsAlias   = "koodam-main." + testNS
	userLines = "Host example\n  User me\n"
)

// sshHome makes a temporary home and a fake runProgram. The fake ssh-keygen writes the key
// files. The fake records each program call.
func sshHome(t *testing.T) (sshPaths, *[][]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var calls [][]string
	old := runProgram
	t.Cleanup(func() { runProgram = old })
	runProgram = func(_ *cobra.Command, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "ssh-keygen" {
			key := args[slices.Index(args, "-f")+1]
			if err := os.WriteFile(key, []byte("private"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(key+".pub", []byte(userKey+" koodam\n"), 0o644)
		}
		return nil
	}
	return sshPaths{dir: filepath.Join(home, ".ssh")}, &calls
}

func testWorkspace(key string, phase corev1alpha1.WorkspacePhase) *corev1alpha1.Workspace {
	return &corev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: wsName, Namespace: testNS},
		Spec:       corev1alpha1.WorkspaceSpec{SSHPublicKeys: []string{otherKey}},
		Status:     corev1alpha1.WorkspaceStatus{Phase: phase, HostKey: key},
	}
}

// runSSH runs koodam with stdin in.
func runSSH(kube client.WithWatch, in string, args ...string) (string, error) {
	e := &env{kube: kube, clientset: kubefake.NewClientset(), namespace: testNS, poll: time.Millisecond}
	root := newRoot(func() (*env, error) { return e, nil })
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetIn(strings.NewReader(in))
	root.SetArgs(args)
	root.SetContext(context.Background())
	err := root.Execute()
	return out.String(), err
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func getWorkspace(t *testing.T, kube client.Client) *corev1alpha1.Workspace {
	t.Helper()
	w := &corev1alpha1.Workspace{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: wsName}, w); err != nil {
		t.Fatal(err)
	}
	return w
}

func wantMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	if onWindows {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Errorf("mode of %s: got %o, want %o", path, fi.Mode().Perm(), want)
	}
}

func backups(t *testing.T, p sshPaths) []string {
	t.Helper()
	b, err := filepath.Glob(p.config() + ".koodam-backup-*")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSSHSetupIsIdempotent(t *testing.T) {
	p, calls := sshHome(t)
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.config(), []byte(userLines), 0o644); err != nil {
		t.Fatal(err)
	}
	kube := newKube(t, nil, testWorkspace(hostKey+" root@pod", corev1alpha1.WorkspaceRunning))
	out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	wantAll(t, out, "Added your public key", "ssh koodam-main")
	files := map[string]string{}
	for _, f := range []string{p.config(), p.hostFile(), p.knownHosts()} {
		files[f] = read(t, f)
	}
	if out, err := runSSH(kube, "", "ssh", "setup", wsName); err != nil {
		t.Fatalf("second setup: %v\n%s", err, out)
	} else if strings.Contains(out, "Added") || strings.Contains(out, "Include") {
		t.Errorf("the second setup made a change:\n%s", out)
	}
	for f, want := range files {
		if got := read(t, f); got != want {
			t.Errorf("the second setup changed %s:\n%s\nwant:\n%s", f, got, want)
		}
	}
	if got := len(*calls); got != 1 {
		t.Errorf("ssh-keygen ran %d times, want 1", got)
	}
	if got := getWorkspace(t, kube).Spec.SSHPublicKeys; !slices.Equal(got, []string{otherKey, userKey}) {
		t.Errorf("keys: got %q", got)
	}
	if got := read(t, p.config()); got != includeLine+"\n"+userLines {
		t.Errorf("config: got %q", got)
	}
	if b := backups(t, p); len(b) != 1 || read(t, b[0]) != userLines {
		t.Errorf("backups: got %q", b)
	}
	if got := read(t, p.knownHosts()); got != wsAlias+" "+hostKey+"\n" {
		t.Errorf("known_hosts: got %q", got)
	}
	wantAll(t, read(t, p.hostFile()), "Host koodam-main "+wsAlias+"\n", "HostKeyAlias "+wsAlias+"\n",
		"User dev\n", "IdentitiesOnly yes\n", "UserKnownHostsFile ~/.ssh/koodam_known_hosts\n",
		" tunnel "+testNS+"/"+wsName+"\n", "ServerAliveInterval 30\n")
	wantMode(t, p.config(), 0o644)
	wantMode(t, p.dir, 0o700)
	wantMode(t, p.hostDir(), 0o700)
	wantMode(t, p.hostFile(), 0o600)
	wantMode(t, p.knownHosts(), 0o600)
}

func TestSSHSetupAsksBeforeTheInclude(t *testing.T) {
	p, _ := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "n\n", "ssh", "setup", wsName); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if _, err := os.Stat(p.config()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("setup wrote %s after a no", p.config())
	}
	if out, err := runSSH(kube, "y\n", "ssh", "setup", wsName); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if got := read(t, p.config()); got != includeLine+"\n" {
		t.Errorf("config: got %q", got)
	}
	wantMode(t, p.config(), 0o600)
	if b := backups(t, p); len(b) != 0 {
		t.Errorf("setup made a backup of a missing file: %q", b)
	}
}

func TestSSHSetupStopsForABadHostKey(t *testing.T) {
	for key, want := range map[string]string{
		"": "is not ready",
		hostKey + "\n@cert-authority * " + hostKey: "not a valid public key",
		"-----BEGIN OPENSSH PRIVATE KEY-----":      "not a valid public key",
	} {
		p, _ := sshHome(t)
		kube := newKube(t, nil, testWorkspace(key, corev1alpha1.WorkspacePending))
		_, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("host key %q: got %v, want %q", key, err, want)
		}
		if _, err := os.Stat(p.knownHosts()); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("host key %q: setup wrote %s", key, p.knownHosts())
		}
	}
}

func TestSSHHostNamesAreUnique(t *testing.T) {
	names := hostNames([]string{"a/main", "b/main", "a/dev"})
	want := map[string]string{"a/main": "koodam-main.a", "b/main": "koodam-main.b", "a/dev": "koodam-dev koodam-dev.a"}
	for k, v := range want {
		if names[k] != v {
			t.Errorf("%s: got %q, want %q", k, names[k], v)
		}
	}
	if !onWindows {
		if got := proxyCommand("/opt/my apps/it's%h/koodam", "a/main"); got !=
			`'/opt/my apps/it'\''s%%h/koodam' tunnel a/main` {
			t.Errorf("proxyCommand: got %s", got)
		}
	}
}

func TestHasIncludeOnlyBeforeTheFirstHost(t *testing.T) {
	for cfg, want := range map[string]bool{
		"Include config.d/koodam\nHost x\n":                   true,
		"# c\n  include ~/.ssh/config.d/*\n":                  true,
		"Host x\n  Include config.d/koodam\n":                 false,
		"Include other\nMatch all\nInclude config.d/koodam\n": false,
	} {
		if got := hasInclude([]byte(cfg)); got != want {
			t.Errorf("%q: got %v, want %v", cfg, got, want)
		}
	}
}

func TestSSHRemoveRemovesTheEntryTheHostKeyAndTheKey(t *testing.T) {
	p, _ := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if err := setKnownHost(p.knownHosts(), "other", hostKey); err != nil {
		t.Fatal(err)
	}
	if out, err := runSSH(kube, "", "ssh", "remove", wsName, "--key"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if got := read(t, p.knownHosts()); got != "other "+hostKey+"\n" {
		t.Errorf("known_hosts: got %q", got)
	}
	if got := read(t, p.hostFile()); got != hostHeader {
		t.Errorf("host file: got %q", got)
	}
	if got := getWorkspace(t, kube).Spec.SSHPublicKeys; !slices.Equal(got, []string{otherKey}) {
		t.Errorf("keys: got %q", got)
	}
	if got := read(t, p.config()); got != includeLine+"\n" {
		t.Errorf("remove changed the config: %q", got)
	}
}

func TestSSHConnectRunsSSH(t *testing.T) {
	_, calls := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if _, err := runSSH(kube, "", "ssh", wsName); err == nil || !strings.Contains(err.Error(), "ssh setup") {
		t.Errorf("connect before setup: got %v", err)
	}
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	*calls = nil
	if out, err := runSSH(kube, "", "ssh", wsName); err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}
	want := []string{sshProgram, "-t", wsAlias, "tmux", "new-session", "-A", "-s", "main"}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Errorf("calls: got %q, want %q", *calls, want)
	}
}
