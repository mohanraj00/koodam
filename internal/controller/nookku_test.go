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
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

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
		c := runJob(a, jobIngInstaller, jobIngGatewayURL).Spec.Template.Spec.Containers[0]
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
		w.Name, w.Namespace = "n", "team"
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
	for _, image := range []string{"", "example.com/koodam:1"} {
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
