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

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The Nookku test folder (#83). Koodam installs no Nookku file at this stage. It only gives the
// folder to the pod, so that the tests stay after the pod stops.
const (
	// nookkuTestsEnv is the env of Nookku for its test folder.
	nookkuTestsEnv = "NOOKKU_TESTS_DIR"
	// nookkuTestsVolume is the Agent volume that the user declares. The CRD rule makes sure that it
	// is a PVC.
	nookkuTestsVolume = "nookku-tests"
	// agentNookkuTestsDir is the mount path of nookkuTestsVolume in the Agent container.
	agentNookkuTestsDir = "/koodam/nookku-tests"
	// workspaceNookkuDir is an empty read-only marker folder. If it is in the pod, the image makes
	// the test folder on the home PVC and exports nookkuTestsEnv in a login shell.
	workspaceNookkuDir = "/etc/koodam/nookku"
)

// hasNookku is true if the harness selects the nookku plugin.
func hasNookku(h *corev1alpha1.HarnessSpec) bool {
	return h != nil && slices.Contains(h.Plugins, corev1alpha1.HarnessPluginNookku)
}

// addAgentNookku mounts the user volume nookku-tests in the agent container and sets the env of the
// folder. invalid refuses an Agent that also sets the env in spec.env.
func addAgentNookku(pod *corev1.PodSpec) {
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: nookkuTestsVolume,
		MountPath: agentNookkuTestsDir})
	c.Env = append(c.Env, corev1.EnvVar{Name: nookkuTestsEnv, Value: agentNookkuTestsDir})
}

// addWorkspaceNookku mounts the marker folder if a harness of the Workspace selects nookku. The
// marker has no data. A Workspace with no nookku plugin gets no change.
func addWorkspaceNookku(pod *corev1.PodSpec, harnesses []corev1alpha1.HarnessSpec) {
	if !slices.ContainsFunc(harnesses, func(h corev1alpha1.HarnessSpec) bool { return hasNookku(&h) }) {
		return
	}
	const vol = "nookku"
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: workspaceNookkuDir,
		ReadOnly: true})
}
