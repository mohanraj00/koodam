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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/harness"
)

// NookkuImageFlag sets the data image of the nookku preset.
const NookkuImageFlag = "nookku-image"

const (
	reasonUnknownPreset = "UnknownPluginPreset"
	msgUnknownPreset    = "The harness names a plugin preset that this controller does not have."
	pathEnv             = "PATH"
	msgNookkuPython     = "The nookku plugin needs python3 3.11 or later on the PATH of the image. The harness did not start."
)

type harnessPreset struct {
	imageFlag            string
	missingImageReason   string
	filesDescription     string
	agentTestMount       corev1.VolumeMount
	agentTestEnv         corev1.EnvVar
	invalidTestVolume    string
	conflictingTestEnv   string
	claudeEnv            corev1.EnvVar
	workspaceMarker      corev1.VolumeMount
	filesMount           corev1.VolumeMount
	copyContainer        string
	copyCommand          []string
	pythonCheckContainer string
}

// The preset name selects an entry. Each image flag, mount, command and variable comes from the entry.
var harnessPresets = map[corev1beta1.HarnessPlugin]harnessPreset{
	"nookku": {
		imageFlag:            NookkuImageFlag,
		missingImageReason:   "NoNookkuImage",
		filesDescription:     "Nookku files",
		agentTestMount:       corev1.VolumeMount{Name: "nookku-tests", MountPath: "/koodam/nookku-tests"},
		agentTestEnv:         corev1.EnvVar{Name: "NOOKKU_TESTS_DIR", Value: "/koodam/nookku-tests"},
		invalidTestVolume:    "The nookku plugin needs the volume nookku-tests in spec.podTemplate.spec.volumes, with a persistentVolumeClaim and a claimName.",
		conflictingTestEnv:   "spec.env cannot set NOOKKU_TESTS_DIR if the harness has the nookku plugin. Koodam sets it.",
		claudeEnv:            corev1.EnvVar{Name: "CLAUDE_CODE_DISABLE_TERMINAL_TITLE", Value: "1"},
		workspaceMarker:      corev1.VolumeMount{Name: "nookku", MountPath: "/etc/koodam/nookku", ReadOnly: true},
		filesMount:           corev1.VolumeMount{Name: "nookku-files", MountPath: "/koodam/nookku", ReadOnly: true},
		copyContainer:        "koodam-nookku-files",
		copyCommand:          []string{"/nookku-copy", "/nookku/share", "/koodam/nookku", "/nookku/bin/nookku", harnessDir + "/nookku"},
		pythonCheckContainer: "koodam-nookku-python",
	},
}

// nookkuPythonScript checks the version of python3 on the PATH.
const nookkuPythonScript = `python3 -c 'import sys; sys.exit(sys.version_info < (3, 11))' >/dev/null 2>&1 && exit 0
echo "` + msgNookkuPython + `" >&2
exit 1`

func unknownPreset(h corev1beta1.HarnessSpec) bool {
	return slices.ContainsFunc(h.Plugins, func(name corev1beta1.HarnessPlugin) bool {
		_, ok := harnessPresets[name]
		return !ok
	})
}

// workspacePresetNames gives each selected preset once, in name order.
func workspacePresetNames(harnesses []corev1beta1.HarnessSpec) []corev1beta1.HarnessPlugin {
	var names []corev1beta1.HarnessPlugin
	for _, h := range harnesses {
		names = append(names, h.Plugins...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (p harnessPreset) agentError(a *corev1beta1.Agent) string {
	if _, set := a.Spec.Env[p.agentTestEnv.Name]; set && p.agentTestEnv.Name != "" {
		return p.conflictingTestEnv
	}
	if p.agentTestMount.Name == "" {
		return ""
	}
	if t := a.Spec.PodTemplate; t != nil {
		for _, v := range t.Spec.Volumes {
			if v.Name == p.agentTestMount.Name && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
				return ""
			}
		}
	}
	return p.invalidTestVolume
}

func (p harnessPreset) addAgent(pod *corev1.PodSpec, harnessName, image string) {
	c := &pod.Containers[0]
	if p.agentTestMount.Name != "" {
		c.VolumeMounts = append(c.VolumeMounts, p.agentTestMount)
		c.Env = append(c.Env, p.agentTestEnv)
	}
	if harnessName == harness.ClaudeCode && p.claudeEnv.Name != "" &&
		!slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == p.claudeEnv.Name }) {
		c.Env = append(c.Env, p.claudeEnv)
	}
	p.addFiles(pod, image)
	p.addPythonCheck(pod)
}

// addFiles copies the pinned files to an emptyDir. It puts the launcher in the harness volume.
// The copy container has no credential or environment variables.
func (p harnessPreset) addFiles(pod *corev1.PodSpec, image string) {
	const harnessVolume = "harness"
	if image == "" || !slices.ContainsFunc(pod.Volumes, func(v corev1.Volume) bool { return v.Name == harnessVolume }) {
		return
	}
	limit := resource.MustParse("64Mi")
	mount := p.filesMount
	mount.ReadOnly = false
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: mount.Name, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}})
	pod.InitContainers = append(pod.InitContainers, corev1.Container{
		Name: p.copyContainer, Image: image, Command: slices.Clone(p.copyCommand),
		VolumeMounts:    []corev1.VolumeMount{mount, {Name: harnessVolume, MountPath: harnessDir}},
		SecurityContext: helperSecurityContext(), Resources: nookkuInitResources(),
	})
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, p.filesMount)
}

// addWorkspace mounts the marker that lets the Workspace image set up its test folder.
func (p harnessPreset) addWorkspace(pod *corev1.PodSpec, image string) {
	if m := p.workspaceMarker; m.Name != "" {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: m.Name, VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		c := &pod.Containers[0]
		c.VolumeMounts = append(c.VolumeMounts, m)
	}
	p.addFiles(pod, image)
	p.addPythonCheck(pod)
}

// addPythonCheck stops the pod if python3 3.11 or later is absent from the image.
func (p harnessPreset) addPythonCheck(pod *corev1.PodSpec) {
	if p.pythonCheckContainer == "" {
		return
	}
	pod.InitContainers = append(pod.InitContainers, corev1.Container{
		Name: p.pythonCheckContainer, Command: []string{"sh", "-c", nookkuPythonScript},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	})
	syncPresetPythonCheck(pod, pod.Containers[0].Name, p.pythonCheckContainer)
}

// nookkuInitResources gives the small fixed requests and limits of the Nookku init containers
// (#367). Thus each one has a CPU and a memory limit, also in a namespace with no LimitRange. The
// copy and the Python check need much less than these limits.
func nookkuInitResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
}

// syncPresetPythonChecks updates the checks after a pod template changes the main container.
func syncPresetPythonChecks(pod *corev1.PodSpec, main string) {
	for _, p := range harnessPresets {
		if p.pythonCheckContainer != "" {
			syncPresetPythonCheck(pod, main, p.pythonCheckContainer)
		}
	}
}

// syncPresetPythonCheck copies the image, pull policy, user, group and allowed PATH from the
// main container. The check runs in "/" and keeps the restrictions of a helper container.
func syncPresetPythonCheck(pod *corev1.PodSpec, main, name string) {
	i := slices.IndexFunc(pod.InitContainers, func(c corev1.Container) bool { return c.Name == name })
	m := slices.IndexFunc(pod.Containers, func(c corev1.Container) bool { return c.Name == main })
	if i < 0 || m < 0 {
		return
	}
	check, c := &pod.InitContainers[i], pod.Containers[m]
	check.Image, check.ImagePullPolicy, check.Env, check.WorkingDir = c.Image, c.ImagePullPolicy, nil, "/"
	sc := helperSecurityContext()
	sc.RunAsNonRoot, sc.RunAsUser = nil, nil
	if m := c.SecurityContext.DeepCopy(); m != nil {
		sc.RunAsNonRoot, sc.RunAsUser, sc.RunAsGroup = m.RunAsNonRoot, m.RunAsUser, m.RunAsGroup
	}
	check.SecurityContext, check.Resources = sc, nookkuInitResources()
	if p := slices.IndexFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == pathEnv }); p >= 0 {
		check.Env = checkPathEnv(c.Env[p])
	}
}

// checkPathEnv gives the PATH env of the main container to the Python check (#367). It copies a
// literal value, a field of the pod and a key of a ConfigMap. It does not copy a key of a Secret,
// because the check must not get a credential, or a resource, because it is not a PATH. In these
// cases the check uses the PATH of the image.
func checkPathEnv(e corev1.EnvVar) []corev1.EnvVar {
	if v := e.ValueFrom; v != nil && v.FieldRef == nil && v.ConfigMapKeyRef == nil {
		return nil
	}
	return []corev1.EnvVar{*e.DeepCopy()}
}
