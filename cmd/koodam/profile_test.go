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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	secretText = "do-not-show-this-text"
	credErr    = "credential"
	claudeFile = "CLAUDE.md"
	keyErr     = "has a private key"
	notesFile  = "notes.md"
)

// profileHome makes a home with CLAUDE.md, AGENTS.md and 1 skill.
func profileHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeTestFile(t, filepath.Join(home, ".claude", claudeFile), "claude rules "+secretText)
	writeTestFile(t, filepath.Join(home, ".codex", "AGENTS.md"), "codex rules")
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "review", "SKILL.md"), "review skill")
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "review", "refs", "a.md"), "ref")
	return home
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func getGlobals(t *testing.T, kube client.Client) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: globalsName}, cm); err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestProfilePushCreatesThenUpdatesTheConfigMap(t *testing.T) {
	home := profileHome(t)
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	wantAll(t, out, "configmap/koodam-globals created in namespace "+testNS, "4 files")
	cm := getGlobals(t, kube)
	if _, ok := cm.Labels[workspaceLabel]; !ok {
		t.Errorf("the ConfigMap has no label %s: %v", workspaceLabel, cm.Labels)
	}
	want := map[string]string{
		claudeFile:                   claudeFile,
		"AGENTS.md":                  "AGENTS.md",
		"skills_sreview_sSKILL.md":   "skills/review/SKILL.md",
		"skills_sreview_srefs_sa.md": "skills/review/refs/a.md",
	}
	var m globalsManifest
	if err := json.Unmarshal([]byte(cm.Data[manifestKey]), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != len(want) || len(cm.Data) != len(want)+1 {
		t.Fatalf("got manifest %+v and %d keys, want %d files", m.Files, len(cm.Data), len(want))
	}
	for _, f := range m.Files {
		if want[f.Key] != f.Path || cm.Data[f.Key] == "" {
			t.Errorf("key %s has path %s, want %s", f.Key, f.Path, want[f.Key])
		}
	}

	// A removed skill goes away at the next push.
	if err := os.RemoveAll(filepath.Join(home, ".claude", "skills", "review", "refs")); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("second push: %v\n%s", err, out)
	}
	wantAll(t, out, "configmap/koodam-globals updated", "3 files")
	if _, ok := getGlobals(t, kube).Data["skills_sreview_srefs_sa.md"]; ok {
		t.Error("the update kept a removed file")
	}
}

func TestProfilePushDryRunShowsKeysAndSizesOnly(t *testing.T) {
	profileHome(t)
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	wantAll(t, out, "CLAUDE.md\t34", "skills_sreview_sSKILL.md\t12", "(dry run)")
	if strings.Contains(out, secretText) {
		t.Errorf("the dry run showed the content:\n%s", out)
	}
	cm := &corev1.ConfigMap{}
	err = kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: globalsName}, cm)
	if !apierrors.IsNotFound(err) {
		t.Errorf("the dry run changed the cluster: %v", err)
	}
}

func TestProfilePushRefusesBadFiles(t *testing.T) {
	cases := map[string]string{
		".credentials.json": credErr,
		"auth.json":         credErr,
		".env":              credErr,
		"ssh.key":           credErr,
		"ID_ED25519":        credErr,
		"Server.PEM":        credErr,
		"cert.p12":          credErr,
		"cert.pfx":          credErr,
		".NETRC":            credErr,
		".npmrc":            credErr,
		".pypirc":           credErr,
		".git-credentials":  credErr,
		"credentials.json":  credErr,
		"token.json":        credErr,
		notesFile:           keyErr,
		"bad name.md":       "not a valid ConfigMap key",
		"logo.png":          keyErr,
		"large.md":          keyErr,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			home := profileHome(t)
			path := filepath.Join(home, ".claude", "skills", "review", name)
			switch name {
			case "logo.png":
				// A binary file with a private key stops the push. It does not skip the skill.
				writeTestFile(t, path, "\xff\xfe\x00-----BEGIN RSA PRIVATE KEY-----"+secretText)
			case "large.md":
				// The mark starts 5 bytes before the end of the 15th block of 64 KiB, after the
				// first 900 KiB. Thus the scan finds a mark across 2 blocks in a file that is too large.
				writeTestFile(t, path, strings.Repeat("x", 15*(64<<10)-16)+"-----BEGIN PRIVATE KEY-----")
			case notesFile:
				writeTestFile(t, path, "-----BEGIN OPENSSH PRIVATE KEY-----\n"+secretText)
			default:
				writeTestFile(t, path, secretText)
			}
			out, err := run(t, newKube(t, nil), "profile", "push")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want an error with %q", err, want)
			}
			if strings.Contains(out+err.Error(), secretText) {
				t.Errorf("the output showed the content:\n%s", out)
			}
		})
	}
}

func TestProfilePushRefusesACredentialFlagPath(t *testing.T) {
	home := profileHome(t)
	path := filepath.Join(home, ".codex", "auth.json")
	writeTestFile(t, path, secretText)
	_, err := run(t, newKube(t, nil), "profile", "push", "--agents-md", path)
	if err == nil || !strings.Contains(err.Error(), credErr) || strings.Contains(err.Error(), secretText) {
		t.Fatalf("got %v, want a credential error without the content", err)
	}
}

func TestProfilePushSkipsSymlinksAndMissingDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	skills := filepath.Join(home, ".claude", "skills")
	writeTestFile(t, filepath.Join(skills, "review", "SKILL.md"), "review skill")
	writeTestFile(t, filepath.Join(home, "outside.md"), secretText)
	if err := os.Symlink(filepath.Join(home, "outside.md"), filepath.Join(skills, "review", "link.md")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.Symlink(home, filepath.Join(skills, "linked")); err != nil {
		t.Fatal(err)
	}
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	real, err := filepath.EvalSymlinks(skills)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out, "skip "+filepath.Join(home, ".claude", claudeFile),
		"skip "+filepath.Join(real, "review", "link.md")+": a symlink",
		"skip "+filepath.Join(real, "linked")+": a symlink", "1 files")
	if data := getGlobals(t, kube).Data; len(data) != 2 || data["skills_sreview_sSKILL.md"] == "" {
		t.Errorf("got keys %v, want only the skill and the manifest", data)
	}
}

func TestProfilePushRefusesAMissingFlagPath(t *testing.T) {
	profileHome(t)
	_, err := run(t, newKube(t, nil), "profile", "push", "--claude-md", filepath.Join(t.TempDir(), "none.md"))
	if err == nil {
		t.Fatal("push accepted a --claude-md path that does not exist")
	}
}

func TestProfilePushGivesAHintOnForbidden(t *testing.T) {
	profileHome(t)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, globalsName,
		os.ErrPermission)
	kube := newKube(t, &interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return forbidden
		},
	})
	_, err := run(t, kube, "profile", "push")
	if err == nil || !apierrors.IsForbidden(err) ||
		!strings.Contains(err.Error(), "get, create and update on configmaps in namespace "+testNS) {
		t.Fatalf("got %v, want Forbidden with the RBAC hint", err)
	}
}

func TestProfilePushRefusesASymlinkOutOfTheFolder(t *testing.T) {
	home := profileHome(t)
	claude := filepath.Join(home, ".claude", claudeFile)
	writeTestFile(t, filepath.Join(home, "outside.md"), secretText)
	if err := os.Remove(claude); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside.md"), claude); err != nil {
		t.Skip("no symlinks:", err)
	}
	out, err := run(t, newKube(t, nil), "profile", "push")
	if err == nil || strings.Contains(out+err.Error(), secretText) {
		t.Fatalf("got %v, want an error without the content", err)
	}
}

func TestCheckRelRefusesUnsafePaths(t *testing.T) {
	for _, rel := range []string{"", "/etc/passwd", "..", "skills/../x", "skills/./x", "skills//x"} {
		if checkRel(rel) == nil {
			t.Errorf("checkRel accepted %q", rel)
		}
	}
	if err := checkRel("skills/review/SKILL.md"); err != nil {
		t.Errorf("checkRel refused a good path: %v", err)
	}
}

func TestKeyEncodingIsReversible(t *testing.T) {
	paths := []string{claudeFile, "skills/review/SKILL.md", "skills/py/__init__.py", "skills/t/_template.md",
		"skills/a_/b", "skills/a/_b", "skills/a__b/c", "skills/x_s/y", "skills/x/_sy", "skills/x_u/y"}
	keys := map[string]string{}
	for _, p := range paths {
		key := encodeKey(p)
		got, err := decodeKey(key)
		if err != nil || got != p {
			t.Errorf("decodeKey(%q) = %q, %v, want %q", key, got, err, p)
		}
		if other, ok := keys[key]; ok {
			t.Errorf("%q and %q give the same key %q", p, other, key)
		}
		keys[key] = p
	}
	if got := encodeKey("skills/py/__init__.py"); got != "skills_spy_s_u_uinit_u_u.py" {
		t.Errorf("got key %q", got)
	}
	for _, bad := range []string{"a_", "a_xb"} {
		if _, err := decodeKey(bad); err == nil {
			t.Errorf("decodeKey accepted %q", bad)
		}
	}
}

func TestProfilePushAcceptsUnderscoreNames(t *testing.T) {
	home := profileHome(t)
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "py", "__init__.py"), "")
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "py", "_template.md"), "template")
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	data := getGlobals(t, kube).Data
	if _, ok := data["skills_spy_s_u_uinit_u_u.py"]; !ok || data["skills_spy_s_utemplate.md"] != "template" {
		t.Errorf("got keys %v", data)
	}
}

func TestGlobalsDataRefusesADuplicateKey(t *testing.T) {
	files := []globalFile{{Key: "k", Path: "a"}, {Key: "k", Path: "b"}}
	if _, err := globalsData(files); err == nil || !strings.Contains(err.Error(), "2 files give the key") {
		t.Fatalf("got %v, want a duplicate key error", err)
	}
}

func TestProfilePushDryRunChecksAndDoesNotConnect(t *testing.T) {
	home := profileHome(t)
	for _, big := range []bool{false, true} {
		if big {
			bigSkills(t, home)
		}
		root := newRoot(func() (*env, error) {
			t.Fatal("the dry run connected to the cluster")
			return nil, nil
		})
		out := &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(out)
		root.SetArgs([]string{"profile", "push", "--dry-run"})
		err := root.Execute()
		if big != (err != nil) || (big && !strings.Contains(err.Error(), "limit is 900 KiB")) {
			t.Fatalf("big %v: got %v\n%s", big, err, out)
		}
	}
}

func TestProfilePushRefusesAConfigMapThatKoodamDidNotMake(t *testing.T) {
	profileHome(t)
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: globalsName},
		Data: map[string]string{"keep": "me"}}
	kube := newKube(t, nil, other)
	_, err := run(t, kube, "profile", "push")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("got %v, want a refusal with the --force hint", err)
	}
	if getGlobals(t, kube).Data["keep"] != "me" {
		t.Fatal("the push changed a ConfigMap that koodam did not make")
	}
	out, err := run(t, kube, "profile", "push", "--force")
	if err != nil {
		t.Fatalf("push --force: %v\n%s", err, out)
	}
	wantAll(t, out, "configmap/koodam-globals updated")
}

func TestProfilePushTellsToRunAgainOnAConflict(t *testing.T) {
	profileHome(t)
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: globalsName,
		Labels: map[string]string{managedByLabel: managedBy}}}
	updates := 0
	kube := newKube(t, &interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			updates++
			return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, globalsName, os.ErrExist)
		},
	}, existing)
	_, err := run(t, kube, "profile", "push")
	if err == nil || !strings.Contains(err.Error(), "run the command again") || updates < 2 {
		t.Fatalf("got %v after %d updates, want retries and a run-again error", err, updates)
	}
}

const binaryBody = "\xff\xfe\x00binary " + secretText

// bigSkills adds 3 skills of 400 KiB. Each skill is below the limit for 1 skill, and the total
// is above the limit.
func bigSkills(t *testing.T, home string) {
	t.Helper()
	for _, n := range []string{"big-a", "big-b", "big-c"} {
		writeTestFile(t, filepath.Join(home, ".claude", "skills", n, "SKILL.md"), strings.Repeat("x", 400<<10))
	}
}

func TestProfilePushSkipsDotEntriesAndBadSkills(t *testing.T) {
	home := profileHome(t)
	skills := filepath.Join(home, ".claude", "skills")
	writeTestFile(t, filepath.Join(skills, ".trash", "id1", "morning", "font.woff2"), binaryBody)
	writeTestFile(t, filepath.Join(skills, ".DS_Store"), binaryBody)
	writeTestFile(t, filepath.Join(skills, "review", ".DS_Store"), binaryBody)
	writeTestFile(t, filepath.Join(skills, "fonts", "SKILL.md"), "fonts skill")
	writeTestFile(t, filepath.Join(skills, "fonts", "assets", "a.woff2"), binaryBody)
	for _, n := range []string{"a.md", "b.md"} {
		writeTestFile(t, filepath.Join(skills, "huge", n), strings.Repeat("x", 500<<10))
	}
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	real, err := filepath.EvalSymlinks(skills)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, out,
		"skip "+filepath.Join(real, ".trash")+": a dot folder",
		"skip "+filepath.Join(real, ".DS_Store")+": a dot file",
		"skip "+filepath.Join(real, "review", ".DS_Store")+": a dot file",
		"skip the skill "+filepath.Join(real, "fonts")+": "+filepath.Join(real, "fonts", "assets", "a.woff2")+
			" is not UTF-8 text",
		"skip the skill "+filepath.Join(real, "huge")+": the skill is larger than 900 KiB, the limit for 1 skill",
		"4 files")
	if got := strings.Count(out, "skip the skill"); got != 2 {
		t.Errorf("got %d skill warnings, want 2:\n%s", got, out)
	}
	if strings.Contains(out, secretText) {
		t.Errorf("the output showed the content:\n%s", out)
	}
	data := getGlobals(t, kube).Data
	if data["skills_sreview_sSKILL.md"] == "" || len(data) != 5 {
		t.Errorf("got keys %v, want the 4 files of the profile and the manifest", data)
	}
}

func TestProfilePushStopsOnACredentialInASkippedSkill(t *testing.T) {
	for _, cred := range []string{"auth.json", notesFile, ".env"} {
		t.Run(cred, func(t *testing.T) {
			home := profileHome(t)
			skill := filepath.Join(home, ".claude", "skills", "fonts")
			// a.woff2 comes before the credential file in the walk, so the skill has a reason to
			// skip before the walk reads the credential file.
			writeTestFile(t, filepath.Join(skill, "a.woff2"), binaryBody)
			writeTestFile(t, filepath.Join(skill, cred), "-----BEGIN OPENSSH PRIVATE KEY-----\n"+secretText)
			kube := newKube(t, nil)
			out, err := run(t, kube, "profile", "push")
			if err == nil || !strings.Contains(err.Error(), filepath.Join("fonts", cred)) {
				t.Fatalf("got %v, want an error for the credential file\n%s", err, out)
			}
			if strings.Contains(out+err.Error(), secretText) {
				t.Errorf("the output showed the content:\n%s", out)
			}
			cm := &corev1.ConfigMap{}
			err = kube.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: globalsName}, cm)
			if !apierrors.IsNotFound(err) {
				t.Errorf("the push changed the cluster: %v", err)
			}
		})
	}
}

// A dot folder is never read and never pushed. Thus a credential file in it does not stop the
// push, and it cannot go to the cluster.
func TestProfilePushDoesNotReadADotFolder(t *testing.T) {
	home := profileHome(t)
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "review", ".cache", "auth.json"), "dot-folder-token")
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	wantAll(t, out, ".cache: a dot folder", "4 files")
	for k, v := range getGlobals(t, kube).Data {
		if strings.Contains(k, "auth") || strings.Contains(v, "dot-folder-token") {
			t.Errorf("the push sent the file in the dot folder as %s", k)
		}
	}
}

func TestProfilePushNamesTheLargestSkills(t *testing.T) {
	home := profileHome(t)
	bigSkills(t, home)
	_, err := run(t, newKube(t, nil), "profile", "push")
	want := "limit is 900 KiB: remove files or skills. The largest skills are big-a (400 KiB), big-b (400 KiB), " +
		"big-c (400 KiB)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestProfilePushDryRunShowsTheSkippedSkills(t *testing.T) {
	home := profileHome(t)
	writeTestFile(t, filepath.Join(home, ".claude", "skills", "fonts", "a.woff2"), binaryBody)
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	wantAll(t, out, "fonts: ", "a.woff2 is not UTF-8 text", "skills_sreview_sSKILL.md\t12", "4 files", "(dry run)")
	if strings.Contains(out, secretText) || strings.Contains(out, "skills_sfonts") {
		t.Errorf("the dry run showed the content or the skipped skill:\n%s", out)
	}
}

func TestProfilePushSkipsAnEmptySkillAndASymlinkInASkill(t *testing.T) {
	home := profileHome(t)
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(skills, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "outside.md"), secretText)
	link := filepath.Join(skills, "review", "refs", "link.md")
	if err := os.Symlink(filepath.Join(home, "outside.md"), link); err != nil {
		t.Skip("no symlinks:", err)
	}
	kube := newKube(t, nil)
	out, err := run(t, kube, "profile", "push")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	wantAll(t, out, "link.md: a symlink", "4 files")
	data := getGlobals(t, kube).Data
	if len(data) != 5 || data["skills_sreview_srefs_sa.md"] != "ref" {
		t.Errorf("got keys %v, want the 4 files of the profile and the manifest", data)
	}
}

func TestProfilePushRefusesABadClaudeOrAgentsFile(t *testing.T) {
	for _, file := range []string{filepath.Join(".claude", claudeFile), filepath.Join(".codex", "AGENTS.md")} {
		for body, want := range map[string]string{
			binaryBody:                        "not UTF-8",
			strings.Repeat("x", maxGlobals+1): "larger than 900 KiB",
		} {
			t.Run(file+" "+want, func(t *testing.T) {
				home := profileHome(t)
				writeTestFile(t, filepath.Join(home, file), body)
				_, err := run(t, newKube(t, nil), "profile", "push")
				if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), secretText) {
					t.Fatalf("got %v, want an error with %q and without the content", err, want)
				}
			})
		}
	}
}

// After a skill is too large, the walk keeps no more data of it, and it still scans the other
// files of the skill for a credential.
func TestWalkSkillsKeepsNoDataOfALargeSkill(t *testing.T) {
	dir := t.TempDir()
	for i := range 20 {
		writeTestFile(t, filepath.Join(dir, "huge", fmt.Sprintf("f%02d.md", i)), strings.Repeat("x", 100<<10))
	}
	writeTestFile(t, filepath.Join(dir, "huge", "z.bin"), binaryBody)
	writeTestFile(t, filepath.Join(dir, "small", "SKILL.md"), "small")
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	got, err := walkSkills(cmd, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].reason == "" || got[0].files != nil || got[0].size > maxSkill+(100<<10) ||
		got[1].reason != "" || len(got[1].files) != 1 {
		t.Fatalf("got %+v and %+v, want a skipped huge skill with no data", *got[0], *got[1])
	}

	writeTestFile(t, filepath.Join(dir, "huge", "z.md"), "-----BEGIN PRIVATE KEY-----\n"+secretText)
	if _, err := walkSkills(cmd, dir); err == nil || !strings.Contains(err.Error(), keyErr) ||
		strings.Contains(err.Error(), secretText) {
		t.Fatalf("got %v, want a private key error after the skip", err)
	}
}
