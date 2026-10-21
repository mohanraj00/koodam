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
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
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
	binKoodam = "/bin/koodam"
)

// sshHome makes a temporary home and a fake runProgram. The fake ssh-keygen writes the key
// files. The fake records each program call.
func sshHome(t *testing.T) (sshPaths, *[][]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var calls [][]string
	old, oldLook := runProgram, lookPath
	t.Cleanup(func() { runProgram, lookPath = old, oldLook })
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
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

// runSSH runs koodam with stdin in and the kube context kind-test.
func runSSH(kube client.WithWatch, in string, args ...string) (string, error) {
	return runSSHEnv(&env{kube: kube, namespace: testNS, context: "kind-test"}, in, args...)
}

// runSSHEnv runs koodam with the env e and stdin in. If e has no clientset, each access review
// is allowed.
func runSSHEnv(e *env, in string, args ...string) (string, error) {
	if e.clientset == nil {
		cs := kubefake.NewClientset()
		allow(cs, true)
		e.clientset = cs
	}
	e.poll = time.Millisecond
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

func allow(cs *kubefake.Clientset, allowed bool) {
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
		})
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
}

func TestProxyCommandQuotesAndPinsTheContext(t *testing.T) {
	if onWindows {
		t.Skip("Windows uses double quotes")
	}
	got, err := proxyCommand("/opt/my apps/it's%h/koodam", "ctx'%r", "/home/me/k 1.yaml", "a/main")
	want := `'/opt/my apps/it'\''s%%h/koodam' --context 'ctx'\''%%r' --kubeconfig '/home/me/k 1.yaml' tunnel a/main`
	if err != nil || got != want {
		t.Errorf("proxyCommand: got %s, %v\nwant %s", got, err, want)
	}
	if got, _ := proxyCommand(binKoodam, "kind", "", "a/main"); got != `'/bin/koodam' --context 'kind' tunnel a/main` {
		t.Errorf("proxyCommand without a kubeconfig: got %s", got)
	}
	for _, args := range [][3]string{{binKoodam + "\n", "kind", ""}, {binKoodam, "kind\nHost *", ""},
		{binKoodam, "", ""}, {binKoodam, "kind", "/k\r"}} {
		if _, err := proxyCommand(args[0], args[1], args[2], "a/main"); err == nil {
			t.Errorf("proxyCommand(%q) did not refuse the input", args)
		}
	}
}

func TestSSHSetupRefusesABadContextName(t *testing.T) {
	p, _ := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	_, err := runSSHEnv(&env{kube: kube, namespace: testNS, context: "kind\nProxyCommand evil"}, "",
		"ssh", "setup", wsName, "--yes")
	if err == nil || !strings.Contains(err.Error(), "kube context name") {
		t.Errorf("got %v, want a refusal of the context name", err)
	}
	if _, err := os.Stat(p.hostFile()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("setup wrote %s", p.hostFile())
	}
	if got := getWorkspace(t, kube).Spec.SSHPublicKeys; !slices.Equal(got, []string{otherKey}) {
		t.Errorf("setup changed the keys: %q", got)
	}
}

func TestSSHSetupKeepsTheContextOfEachEntry(t *testing.T) {
	p, _ := sshHome(t)
	other := testWorkspace(hostKey, corev1alpha1.WorkspaceRunning)
	other.Name = "dev"
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning), other)
	if out, err := runSSHEnv(&env{kube: kube, namespace: testNS, context: "first"}, "", "ssh", "setup", wsName,
		"--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, err := runSSHEnv(&env{kube: kube, namespace: testNS, context: "second", kubeconfig: "/k.yaml"}, "",
		"ssh", "setup", "dev", "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	wantAll(t, read(t, p.hostFile()), " --context 'first' tunnel "+testNS+"/main\n",
		" --context 'second' --kubeconfig '/k.yaml' tunnel "+testNS+"/dev\n")
}

func TestSSHSetupTellsThatTheEntryIsNotActiveAfterANo(t *testing.T) {
	sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	out, err := runSSH(kube, "no\n", "ssh", "setup", wsName)
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	wantAll(t, out, "is not active", "\n  "+includeLine+"\n")
	if strings.Contains(out, "SSH is configured") {
		t.Errorf("setup said that SSH is configured:\n%s", out)
	}
}

func TestSSHSetupKeepsASymlinkedConfig(t *testing.T) {
	if onWindows {
		t.Skip("symlinks need a privilege on Windows")
	}
	p, _ := sshHome(t)
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "dotfiles-ssh-config")
	if err := os.WriteFile(target, []byte(userLines), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.config()); err != nil {
		t.Fatal(err)
	}
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if fi, err := os.Lstat(p.config()); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("%s is not a symlink now: %v", p.config(), err)
	}
	if got := read(t, target); got != includeLine+"\n"+userLines {
		t.Errorf("target: got %q", got)
	}
	wantMode(t, target, 0o644)
}

func TestKubeEnvGivesTheContextAndTheKubeconfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := "apiVersion: v1\nkind: Config\ncurrent-context: a\n" +
		"clusters:\n- name: c\n  cluster: {server: 'https://127.0.0.1:1'}\n" +
		"users:\n- name: u\n  user: {}\n" +
		"contexts:\n- name: a\n  context: {cluster: c, user: u}\n- name: b\n  context: {cluster: c, user: u}\n"
	if err := os.WriteFile("kubeconfig", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{"": "a", "b": "b"} {
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: "kubeconfig"}
		e, err := kubeEnv(rules, &clientcmd.ConfigOverrides{CurrentContext: flag})
		if err != nil {
			t.Fatal(err)
		}
		if e.context != want || e.kubeconfig != filepath.Join(dir, "kubeconfig") {
			t.Errorf("--context %q: got %q and %q", flag, e.context, e.kubeconfig)
		}
	}
}

func TestSetKnownHostRemovesCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("other "+hostKey+"\r\nold "+hostKey+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setKnownHost(path, "old", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "other "+hostKey+"\n" {
		t.Errorf("got %q", got)
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

func TestSSHRemoveWithoutKeyKeepsTheKeys(t *testing.T) {
	p, _ := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, err := runSSH(kube, "", "ssh", "remove", wsName); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if got := getWorkspace(t, kube).Spec.SSHPublicKeys; !slices.Equal(got, []string{otherKey, userKey}) {
		t.Errorf("keys: got %q", got)
	}
	if got := read(t, p.hostFile()); got != hostHeader {
		t.Errorf("host file: got %q", got)
	}
}

func TestSSHConnectAndTestRunSSH(t *testing.T) {
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
	out, err := runSSH(kube, "", "ssh", "test", wsName)
	if err != nil {
		t.Fatalf("test: %v\n%s", err, out)
	}
	wantAll(t, out, "The SSH test passed")
	want := [][]string{{sshProgram, "-t", wsAlias, "tmux", "new-session", "-A", "-s", "main"},
		{sshProgram, wsAlias, testScript}}
	if len(*calls) != 2 || !slices.Equal((*calls)[0], want[0]) || !slices.Equal((*calls)[1], want[1]) {
		t.Errorf("calls: got %q, want %q", *calls, want)
	}
}

func TestSSHDoctorFindsEachFault(t *testing.T) {
	p, _ := sshHome(t)
	ok := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(ok, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, err := runSSH(ok, "", "ssh", "doctor"); err != nil || strings.Contains(out, "FAULT") {
		t.Fatalf("doctor on a good setup: %v\n%s", err, out)
	}

	if err := os.WriteFile(p.config(), []byte(userLines+includeLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.key(), 0o644); err != nil {
		t.Fatal(err)
	}
	suspended := testWorkspace(hostKey, corev1alpha1.WorkspaceSuspended)
	suspended.Spec.Suspend = true
	cs := kubefake.NewClientset()
	allow(cs, false)
	out, err := runSSHEnv(&env{kube: newKube(t, nil, suspended), clientset: cs, namespace: testNS}, "", "ssh", "doctor")
	if err == nil {
		t.Fatalf("doctor found no fault:\n%s", out)
	}
	faults := []string{"Include", "mode of " + p.key(), "pods/portforward", "is not suspended", "is Running"}
	if onWindows {
		faults = slices.Delete(faults, 1, 2)
	}
	wantFaults(t, out, faults...)

	root := newRoot(func() (*env, error) { return nil, errors.New("no context") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"ssh", "doctor"})
	if err := root.Execute(); err == nil || !strings.Contains(buf.String(), "FAULT  the kube context") {
		t.Errorf("doctor with no kube context: %v\n%s", err, buf.String())
	}
}

// wantFaults checks that out has a FAULT line that contains each of faults.
func wantFaults(t *testing.T, out string, faults ...string) {
	t.Helper()
	for _, f := range faults {
		if !slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool {
			return strings.HasPrefix(l, "FAULT") && strings.Contains(l, f)
		}) {
			t.Errorf("no FAULT line for %q:\n%s", f, out)
		}
	}
}

func TestSSHDoctorFindsAMissingSetup(t *testing.T) {
	p, _ := sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	out, err := runSSH(kube, "", "ssh", "doctor")
	if err == nil {
		t.Fatalf("doctor found no fault:\n%s", out)
	}
	wantFaults(t, out, p.key()+" exists", p.hostFile()+" exists", "has a Workspace")
	out, err = runSSH(kube, "", "ssh", "doctor", wsName)
	if err == nil {
		t.Fatalf("doctor %s found no fault:\n%s", wsName, out)
	}
	wantFaults(t, out, "has an entry for "+testNS+"/"+wsName+": run koodam ssh setup -n "+testNS+" "+wsName)
}

func TestSSHDoctorChecksTheWorkspaceArgument(t *testing.T) {
	sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	out, err := runSSH(kube, "", "ssh", "doctor", wsName)
	if err != nil || strings.Contains(out, "FAULT") {
		t.Fatalf("doctor %s: %v\n%s", wsName, err, out)
	}
	wantAll(t, out, "ok     ~/.ssh/config.d/koodam has an entry for "+testNS+"/"+wsName, "is Running")
}

func TestSSHDoctorShowsTheAccessReviewError(t *testing.T) {
	sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	cs := kubefake.NewClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("token secret-value"))
		})
	out, err := runSSHEnv(&env{kube: kube, clientset: cs, namespace: testNS}, "", "ssh", "doctor")
	if err == nil {
		t.Fatalf("doctor found no fault:\n%s", out)
	}
	wantFaults(t, out, "pods/portforward in namespace "+testNS+": create the SelfSubjectAccessReview: InternalError")
	if strings.Contains(out, "secret-value") || strings.Contains(out, "ask the cluster admin") {
		t.Errorf("doctor showed the server message or the RBAC hint:\n%s", out)
	}
}

func TestSSHTestFailureNamesTheNamespace(t *testing.T) {
	sshHome(t)
	kube := newKube(t, nil, testWorkspace(hostKey, corev1alpha1.WorkspaceRunning))
	if out, err := runSSH(kube, "", "ssh", "setup", wsName, "--yes"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	runProgram = func(*cobra.Command, string, ...string) error { return errors.New("exit status 255") }
	out, err := runSSH(kube, "", "ssh", "test", wsName)
	want := "run koodam ssh doctor -n " + testNS + " " + wsName
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(out, "passed") {
		t.Errorf("got %v, want %q\n%s", err, want, out)
	}
}
