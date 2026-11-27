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

package controller

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The expected values are literals, not the production constants, so a change to a production
// value makes the tests fail.
const (
	nookkuTestPlugin   = "nookku"
	nookkuTestVolume   = "nookku-tests"
	nookkuTestEnv      = "NOOKKU_TESTS_DIR"
	nookkuTestAgentDir = "/koodam/nookku-tests"
	nookkuTestMarker   = "/etc/koodam/nookku"
	nookkuTestOther    = "/tmp/other"
)

// nookkuAgent is an Agent with the harness h, the nookku plugin if on, and a template with the
// user PVC nookku-tests.
func nookkuAgent(h string, on bool) *corev1alpha1.Agent {
	tmpl := &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: nookkuTestVolume,
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: "my-tests"}}}}}}
	a := jobIngAgent(nil, h, tmpl)
	if on {
		a.Spec.Harness.Plugins = []corev1alpha1.HarnessPlugin{nookkuTestPlugin}
	}
	return a
}

func nookkuMount(c corev1.Container) (corev1.VolumeMount, bool) {
	i := slices.IndexFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == nookkuTestVolume })
	if i < 0 {
		return corev1.VolumeMount{}, false
	}
	return c.VolumeMounts[i], true
}

func nookkuEnv(c corev1.Container) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(c.Env))
	for _, e := range c.Env {
		if e.Name == nookkuTestEnv {
			out = append(out, e)
		}
	}
	return out
}

func TestAgentNookkuFolder(t *testing.T) {
	for _, h := range []string{claudeName, codexName} {
		_, out, err := jobIngMerge(t, nookkuAgent(h, true))
		if err != nil {
			t.Fatalf("%s: the merge failed: %v", h, err)
		}
		c := out.Spec.Containers[0]
		m, ok := nookkuMount(c)
		if !ok || m.MountPath != nookkuTestAgentDir || m.ReadOnly || m.SubPath != "" {
			t.Errorf("%s: the mount of nookku-tests is %+v, want a writable mount at %s", h, m, nookkuTestAgentDir)
		}
		if env := nookkuEnv(c); len(env) != 1 || env[0].Value != nookkuTestAgentDir || env[0].ValueFrom != nil {
			t.Errorf("%s: the env %s is %+v, want one value %s", h, nookkuTestEnv, env, nookkuTestAgentDir)
		}
		// The mount refers to the user PVC, and Koodam adds no volume of the same name.
		n := 0
		for _, v := range out.Spec.Volumes {
			if v.Name == nookkuTestVolume {
				n++
				if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "my-tests" {
					t.Errorf("%s: the volume nookku-tests is not the user PVC: %+v", h, v.VolumeSource)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: the pod has %d volumes nookku-tests, want 1", h, n)
		}
		for _, ic := range out.Spec.InitContainers {
			if _, ok := nookkuMount(ic); ok {
				t.Errorf("%s: the init container %s mounts nookku-tests", h, ic.Name)
			}
		}
	}
}

func TestAgentNoNookkuFolder(t *testing.T) {
	for _, a := range []*corev1alpha1.Agent{nookkuAgent(claudeName, false), nookkuAgent(codexName, false),
		jobIngAgent(nil, "", nil)} {
		c := runJob(a, jobIngInstaller, jobIngNookku, jobIngGatewayURL).Spec.Template.Spec.Containers[0]
		if _, ok := nookkuMount(c); ok {
			t.Error("an Agent with no nookku plugin mounts nookku-tests")
		}
		if len(nookkuEnv(c)) != 0 {
			t.Error("an Agent with no nookku plugin has the env " + nookkuTestEnv)
		}
	}
}

func TestAgentNookkuRefusesUserValues(t *testing.T) {
	a := nookkuAgent(claudeName, true)
	a.Spec.Env = map[string]string{nookkuTestEnv: nookkuTestOther}
	if invalid(a) == "" {
		t.Error("spec.env with " + nookkuTestEnv + " and the nookku plugin is valid")
	}
	off := nookkuAgent(claudeName, false)
	off.Spec.Env = map[string]string{nookkuTestEnv: nookkuTestOther}
	if msg := invalid(off); msg != "" {
		t.Errorf("spec.env with %s and no plugin is not valid: %s", nookkuTestEnv, msg)
	}

	// The user main container cannot set the env or mount a volume at the folder.
	for name, c := range map[string]corev1.Container{
		"env": {Name: agentContainer, Env: []corev1.EnvVar{{Name: nookkuTestEnv, Value: nookkuTestOther}}},
		"mount": {Name: agentContainer, VolumeMounts: []corev1.VolumeMount{{Name: nookkuTestVolume,
			MountPath: nookkuTestAgentDir + "/x"}}},
	} {
		a := nookkuAgent(codexName, true)
		a.Spec.PodTemplate.Spec.Containers = []corev1.Container{c}
		if _, _, err := jobIngMerge(t, a); !isInvalidTemplate(err) {
			t.Errorf("%s: the merge error is %v, want an invalid template", name, err)
		}
	}
}

func TestWorkspaceNookkuMarker(t *testing.T) {
	ws := func(h ...corev1alpha1.HarnessSpec) *corev1alpha1.Workspace {
		w := &corev1alpha1.Workspace{Spec: corev1alpha1.WorkspaceSpec{Harnesses: h}}
		w.Name, w.Namespace = "n", testTeam
		return w
	}
	on := corev1alpha1.HarnessSpec{Name: codexName, Plugins: []corev1alpha1.HarnessPlugin{nookkuTestPlugin}}
	off := corev1alpha1.HarnessSpec{Name: claudeName}
	marker := func(p corev1.PodSpec) (vols, mounts int, ok bool) {
		ok = true
		for _, m := range p.Containers[0].VolumeMounts {
			if m.MountPath == nookkuTestMarker {
				mounts++
				ok = ok && m.ReadOnly
				i := slices.IndexFunc(p.Volumes, func(v corev1.Volume) bool { return v.Name == m.Name })
				ok = ok && i >= 0 && p.Volumes[i].EmptyDir != nil
				if i >= 0 {
					vols++
				}
			}
		}
		return vols, mounts, ok
	}
	// The marker does not need an installer image: it is the folder, not the install.
	for _, image := range []string{"", installer} {
		r := &WorkspaceReconciler{Image: testWorkspaceImage, InstallerImage: image}
		for name, c := range map[string]struct {
			w    *corev1alpha1.Workspace
			want int
		}{
			"none": {ws(), 0}, "off": {ws(off), 0}, "on": {ws(on), 1}, "second": {ws(off, on), 1},
		} {
			vols, mounts, ok := marker(r.statefulSet(c.w, testProfile).Spec.Template.Spec)
			if mounts != c.want || vols != c.want || !ok {
				t.Errorf("image %q, %s: %d mounts and %d volumes of the marker (read-only emptyDir %v), want %d",
					image, name, mounts, vols, ok, c.want)
			}
		}
	}
}

// nookkuTestCheck is the name of the init container that checks the Python of the image.
const nookkuTestCheck = "koodam-nookku-python"

// nookkuCheck gives the Python check init containers of the pod.
func nookkuCheck(p corev1.PodSpec) []corev1.Container {
	var out []corev1.Container
	for _, c := range p.InitContainers {
		if c.Name == nookkuTestCheck {
			out = append(out, c)
		}
	}
	return out
}

// checkNookkuCheck examines one Python check: the image and the PATH of the main container, a
// fixed command, and no credential, mount or privilege.
func checkNookkuCheck(t *testing.T, name string, p corev1.PodSpec) {
	t.Helper()
	checks, main := nookkuCheck(p), p.Containers[0]
	if len(checks) != 1 {
		t.Fatalf("%s: the pod has %d Python checks, want 1", name, len(checks))
	}
	c := checks[0]
	if c.Image != main.Image || c.ImagePullPolicy != main.ImagePullPolicy || len(c.Command) != 3 || c.Command[0] != "sh" || c.Command[1] != "-c" ||
		len(c.Args) != 0 || c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Errorf("%s: the Python check is %+v", name, c)
	}
	if len(c.EnvFrom) != 0 || len(c.VolumeMounts) != 0 || len(c.Ports) != 0 {
		t.Errorf("%s: the Python check has an env source, a mount or a port", name)
	}
	checkNookkuResources(t, name+": the Python check", c)
	if wantEnv := wantCheckEnv(main.Env); !reflect.DeepEqual(c.Env, wantEnv) {
		t.Errorf("%s: the env of the Python check is %+v, want %+v", name, c.Env, wantEnv)
	}
	if s := c.SecurityContext; s == nil || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation ||
		s.Capabilities == nil || len(s.Capabilities.Drop) != 1 || s.Privileged != nil && *s.Privileged ||
		s.ReadOnlyRootFilesystem == nil || !*s.ReadOnlyRootFilesystem {
		t.Errorf("%s: the security context of the Python check is %+v", name, s)
	}
	// The check runs as the user of the main container, so it finds the same python3.
	var want corev1.SecurityContext
	if m := main.SecurityContext; m != nil {
		want = corev1.SecurityContext{RunAsNonRoot: m.RunAsNonRoot, RunAsUser: m.RunAsUser, RunAsGroup: m.RunAsGroup}
	}
	if s := c.SecurityContext; s != nil && (!reflect.DeepEqual(s.RunAsUser, want.RunAsUser) ||
		!reflect.DeepEqual(s.RunAsGroup, want.RunAsGroup) || !reflect.DeepEqual(s.RunAsNonRoot, want.RunAsNonRoot)) ||
		c.WorkingDir != "/" {
		t.Errorf("%s: the Python check does not run as the main container", name)
	}
}

// wantCheckEnv gives the env of the Python check for the env of the main container: only a PATH
// with a literal value, a field of the pod or a key of a ConfigMap.
func wantCheckEnv(env []corev1.EnvVar) []corev1.EnvVar {
	for _, e := range env {
		if v := e.ValueFrom; e.Name == pathEnv && (v == nil || v.FieldRef != nil || v.ConfigMapKeyRef != nil) {
			return []corev1.EnvVar{e}
		}
	}
	return nil
}

// checkNookkuResources examines the fixed requests and limits of a Nookku init container (#367).
// They do not come from the main container.
func checkNookkuResources(t *testing.T, name string, c corev1.Container) {
	t.Helper()
	r := c.Resources
	for _, w := range []struct {
		kind string
		got  corev1.ResourceList
		res  corev1.ResourceName
		want string
	}{{"request", r.Requests, corev1.ResourceCPU, "10m"}, {"request", r.Requests, corev1.ResourceMemory, "16Mi"},
		{"limit", r.Limits, corev1.ResourceCPU, "100m"}, {"limit", r.Limits, corev1.ResourceMemory, "64Mi"}} {
		if q, ok := w.got[w.res]; !ok || q.String() != w.want {
			t.Errorf("%s: the %s %s is %v, want %s", name, w.res, w.kind, w.got[w.res], w.want)
		}
	}
	if len(r.Requests) != 2 || len(r.Limits) != 2 || len(r.Claims) != 0 {
		t.Errorf("%s: the resources are %+v, want only the CPU and memory", name, r)
	}
}

func TestAgentNookkuPythonCheck(t *testing.T) {
	for _, h := range []string{claudeName, codexName} {
		_, out, err := jobIngMerge(t, nookkuAgent(h, true))
		if err != nil {
			t.Fatalf("%s: the merge failed: %v", h, err)
		}
		checkNookkuCheck(t, h, out.Spec)
		off := runJob(nookkuAgent(h, false), jobIngInstaller, jobIngNookku, jobIngGatewayURL).Spec.Template.Spec
		if len(nookkuCheck(off)) != 0 {
			t.Errorf("%s: an Agent with no nookku plugin has a Python check", h)
		}
	}
}

func TestWorkspaceNookkuPythonCheck(t *testing.T) {
	w := &corev1alpha1.Workspace{Spec: corev1alpha1.WorkspaceSpec{Harnesses: []corev1alpha1.HarnessSpec{
		{Name: claudeName}, {Name: codexName, Plugins: []corev1alpha1.HarnessPlugin{nookkuTestPlugin}}}}}
	w.Name, w.Namespace = "n", testTeam
	r := &WorkspaceReconciler{Image: testWorkspaceImage, InstallerImage: installer}
	checkNookkuCheck(t, "on", r.statefulSet(w, testProfile).Spec.Template.Spec)
	w.Spec.Harnesses = w.Spec.Harnesses[:1]
	if len(nookkuCheck(r.statefulSet(w, testProfile).Spec.Template.Spec)) != 0 {
		t.Error("a Workspace with no nookku plugin has a Python check")
	}
}

// TestNookkuPythonCheckFollowsMerge checks that the Python check gets the image and the PATH of the
// main container after spec.podTemplate changes them. A literal PATH is copied, and a PATH from a
// source is not.
func TestNookkuPythonCheckFollowsMerge(t *testing.T) {
	pod := corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.com/a:1",
		Env: []corev1.EnvVar{{Name: pathEnv, Value: "/a"}}}}}
	addNookkuPythonCheck(&pod)
	pod.Containers[0].Image = "example.com/b:1"
	pod.Containers[0].Env = []corev1.EnvVar{{Name: pathEnv, Value: "/b"}, {Name: "OTHER", Value: "x"}}
	pod.Containers[0].WorkingDir, pod.Containers[0].ImagePullPolicy = "/work", corev1.PullNever
	pod.Containers[0].SecurityContext = &corev1.SecurityContext{RunAsUser: new(int64(0)),
		RunAsGroup: new(int64(10)), Privileged: new(true), Capabilities: &corev1.Capabilities{
			Add: []corev1.Capability{"NET_ADMIN"}}}
	syncNookkuPythonCheck(&pod, "main")
	if c := nookkuCheck(pod)[0]; c.Image != "example.com/b:1" || !slices.Equal(c.Env, []corev1.EnvVar{{Name: pathEnv, Value: "/b"}}) {
		t.Errorf("the Python check is %+v after the merge", c)
	}
	// The check gets the user and the group, but no privilege or capability of the main container.
	checkNookkuCheck(t, "merged", pod)
	if s := nookkuCheck(pod)[0].SecurityContext; s.Privileged != nil || len(s.Capabilities.Add) != 0 {
		t.Errorf("the Python check gets a privilege of the main container: %+v", s)
	}
	if pod.Containers[0].SecurityContext.RunAsUser == nookkuCheck(pod)[0].SecurityContext.RunAsUser {
		t.Error("the Python check shares the security context of the main container")
	}
	// A PATH from a ConfigMap or a field of the pod is copied (#367). A PATH from a Secret or a
	// resource is not.
	cm := &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "paths"}, Key: "path", Optional: new(true)}}
	field := &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['path']"}}
	secret := &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "token"}, Key: "path"}}
	res := &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{Resource: "limits.cpu"}}
	// A slice, not a map, so that the resource case is last.
	for _, c := range []struct {
		name string
		from *corev1.EnvVarSource
		copy bool
	}{{"ConfigMap", cm, true}, {"field", field, true}, {"Secret", secret, false}, {"resource", res, false}} {
		name := c.name
		pod.Containers[0].Env = []corev1.EnvVar{{Name: "LANG", Value: "C"}, {Name: pathEnv, ValueFrom: c.from}}
		syncNookkuPythonCheck(&pod, "main")
		var want []corev1.EnvVar
		if c.copy {
			want = []corev1.EnvVar{{Name: pathEnv, ValueFrom: c.from}}
		}
		if got := nookkuCheck(pod)[0].Env; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the Python check gets the env %+v, want %+v", name, got, want)
		}
		checkNookkuCheck(t, name, pod)
	}
	if c := nookkuCheck(pod)[0]; c.Env != nil || pod.Containers[0].Env[1].ValueFrom != res {
		t.Errorf("the merge changes the env of the main container: %+v", pod.Containers[0].Env)
	}
	// The check keeps its fixed resources if the main container has larger ones.
	pod.Containers[0].Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}}
	syncNookkuPythonCheck(&pod, "main")
	checkNookkuResources(t, "large main", nookkuCheck(pod)[0])
}

// TestNookkuPythonScript runs the script of the Python check with a fake python3. The fake gives the
// exit code of the version test.
func TestNookkuPythonScript(t *testing.T) {
	const want = "The nookku plugin needs python3 3.11 or later on the PATH of the image. The harness did not start.\n"
	for name, c := range map[string]struct {
		fake string
		ok   bool
	}{
		"new": {"exit 0", true}, "old": {"exit 1", false}, "broken": {"exit 2", false}, "absent": {"", false},
	} {
		dir := t.TempDir()
		if c.fake != "" {
			if err := os.WriteFile(filepath.Join(dir, "python3"), []byte("#!/bin/sh\n"+c.fake+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("/bin/sh", "-c", nookkuPythonScript)
		cmd.Env = []string{"PATH=" + dir}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if c.ok && (err != nil || stderr.Len() != 0) {
			t.Errorf("%s: the check failed: %v %q", name, err, stderr.String())
		}
		if !c.ok && (err == nil || stderr.String() != want) {
			t.Errorf("%s: got %v and %q, want a failure with the fixed message", name, err, stderr.String())
		}
	}
}

// The names and paths of the Nookku file copy, and the Claude Code env (#83, D5 and D6).
const (
	nookkuTestFiles    = "koodam-nookku-files"
	nookkuTestFilesVol = "nookku-files"
	nookkuTestDir      = "/koodam/nookku"
	nookkuTestTitle    = "CLAUDE_CODE_DISABLE_TERMINAL_TITLE"
	// installer is the installer image that the Workspace tests use.
	installer = "example.com/koodam:1"
)

// checkNookkuFiles examines the copy of the pinned Nookku files: the data image, a fixed command,
// the 2 volumes, no credential and a read-only mount in the main container.
func checkNookkuFiles(t *testing.T, name, image string, p corev1.PodSpec) {
	t.Helper()
	var files []corev1.Container
	for _, c := range p.InitContainers {
		if c.Name == nookkuTestFiles {
			files = append(files, c)
		}
	}
	if len(files) != 1 {
		t.Fatalf("%s: the pod has %d Nookku file copies, want 1", name, len(files))
	}
	c := files[0]
	if c.Image != image || !slices.Equal(c.Command, []string{"/nookku-copy", "/nookku/share", nookkuTestDir,
		"/nookku/bin/nookku", "/koodam/bin/nookku"}) || len(c.Args) != 0 {
		t.Errorf("%s: the Nookku file copy is %+v", name, c)
	}
	if len(c.Env) != 0 || len(c.EnvFrom) != 0 || !reflect.DeepEqual(c.VolumeMounts, []corev1.VolumeMount{
		{Name: nookkuTestFilesVol, MountPath: nookkuTestDir}, {Name: "harness", MountPath: "/koodam/bin"}}) {
		t.Errorf("%s: the Nookku file copy has an env or other mounts: %+v", name, c)
	}
	if s := c.SecurityContext; s == nil || s.RunAsNonRoot == nil || !*s.RunAsNonRoot ||
		s.ReadOnlyRootFilesystem == nil || !*s.ReadOnlyRootFilesystem {
		t.Errorf("%s: the Nookku file copy is not a helper: %+v", name, s)
	}
	checkNookkuResources(t, name+": the Nookku file copy", c)
	want := corev1.VolumeMount{Name: nookkuTestFilesVol, MountPath: nookkuTestDir, ReadOnly: true}
	if !slices.ContainsFunc(p.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return reflect.DeepEqual(m, want) }) {
		t.Errorf("%s: the main container does not mount the Nookku files read-only", name)
	}
	if !slices.ContainsFunc(p.Volumes, func(v corev1.Volume) bool { return v.Name == nookkuTestFilesVol && v.EmptyDir != nil }) {
		t.Errorf("%s: the pod has no emptyDir nookku-files", name)
	}
}

// TestAgentNookkuFiles checks the copy of the Nookku files and the env of Claude Code (#83, D5 and
// D6), also after the pod template merge.
func TestAgentNookkuFiles(t *testing.T) {
	for _, h := range []string{claudeName, codexName} {
		_, out, err := jobIngMerge(t, nookkuAgent(h, true))
		if err != nil {
			t.Fatalf("%s: the merge failed: %v", h, err)
		}
		checkNookkuFiles(t, h, jobIngNookku, out.Spec)
		title := slices.ContainsFunc(out.Spec.Containers[0].Env, func(e corev1.EnvVar) bool {
			return e == corev1.EnvVar{Name: nookkuTestTitle, Value: "1"}
		})
		if title != (h == claudeName) {
			t.Errorf("%s: the env CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1 is %v", h, title)
		}
		off := runJob(nookkuAgent(h, false), jobIngInstaller, jobIngNookku, jobIngGatewayURL).Spec.Template.Spec
		if slices.ContainsFunc(off.InitContainers, func(c corev1.Container) bool { return c.Name == nookkuTestFiles }) ||
			slices.ContainsFunc(off.Containers[0].Env, func(e corev1.EnvVar) bool { return e.Name == nookkuTestTitle }) {
			t.Errorf("%s: an Agent with no nookku plugin gets Nookku files or the title env", h)
		}
	}
	// A value of spec.env stays.
	a := nookkuAgent(claudeName, true)
	a.Spec.Env = map[string]string{nookkuTestTitle: "0"}
	env := runJob(a, jobIngInstaller, jobIngNookku, jobIngGatewayURL).Spec.Template.Spec.Containers[0].Env
	if n := slices.IndexFunc(env, func(e corev1.EnvVar) bool { return e.Name == nookkuTestTitle }); n < 0 ||
		env[n].Value != "0" || slices.ContainsFunc(env[n+1:], func(e corev1.EnvVar) bool { return e.Name == nookkuTestTitle }) {
		t.Errorf("the env of spec.env changed: %+v", env)
	}
	// With no Nookku image, the controller refuses the run.
	r := &AgentReconciler{InstallerImage: jobIngInstaller}
	if r.noInstaller(nookkuAgent(codexName, true)) != msgNoNookkuImage || r.noInstaller(nookkuAgent(codexName, false)) != "" {
		t.Error("the controller does not refuse an Agent with nookku and no Nookku image")
	}
}

func TestWorkspaceNookkuFiles(t *testing.T) {
	w := &corev1alpha1.Workspace{Spec: corev1alpha1.WorkspaceSpec{Harnesses: []corev1alpha1.HarnessSpec{
		{Name: claudeName}, {Name: codexName, Plugins: []corev1alpha1.HarnessPlugin{nookkuTestPlugin}}}}}
	w.Name, w.Namespace = "n", testTeam
	r := &WorkspaceReconciler{Image: testWorkspaceImage, InstallerImage: installer, NookkuImage: jobIngNookku}
	spec := r.statefulSet(w, testProfile).Spec.Template.Spec
	checkNookkuFiles(t, "on", jobIngNookku, spec)
	if slices.ContainsFunc(spec.Containers[0].Env, func(e corev1.EnvVar) bool { return e.Name == nookkuTestTitle }) {
		t.Error("a Workspace container gets the title env")
	}
	// With no Nookku image or no installer image, the pod has no copy.
	for _, r := range []*WorkspaceReconciler{{Image: testWorkspaceImage, InstallerImage: installer},
		{Image: testWorkspaceImage, NookkuImage: jobIngNookku}} {
		if spec := r.statefulSet(w, testProfile).Spec.Template.Spec; slices.ContainsFunc(spec.InitContainers,
			func(c corev1.Container) bool { return c.Name == nookkuTestFiles }) {
			t.Errorf("the pod has a Nookku file copy with the images %q and %q", r.InstallerImage, r.NookkuImage)
		}
	}
}
