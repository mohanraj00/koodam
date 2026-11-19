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

// The Nookku preset (#83). Koodam installs no Nookku file at this stage. It gives the test folder
// to the pod, so that the tests stay after the pod stops, and it checks the Python of the image.
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
	// nookkuPythonContainer is the init container that checks the Python of the harness image.
	nookkuPythonContainer = "koodam-nookku-python"
	// pathEnv is the name of the env that lists the command folders.
	pathEnv = "PATH"
	// msgNookkuPython is the termination message of nookkuPythonContainer if the check fails.
	msgNookkuPython = "The nookku plugin needs python3 3.10 or later on the PATH of the image. The harness did not start."
)

// nookkuPythonScript exits with 0 only if python3 on the PATH is 3.10 or later. It runs no Nookku
// code and reads no file.
const nookkuPythonScript = `python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))' >/dev/null 2>&1 && exit 0
echo "` + msgNookkuPython + `" >&2
exit 1`

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
	addNookkuPythonCheck(pod)
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
	addNookkuPythonCheck(pod)
}

// addNookkuPythonCheck adds an init container that stops the pod before the harness starts if the
// image of the main container has no python3 3.10 or later (#83). Nookku needs Python 3.10 or later,
// and a hook that cannot start Nookku must not let the harness run. The container gets no
// credential, no mount and no env but PATH.
func addNookkuPythonCheck(pod *corev1.PodSpec) {
	pod.InitContainers = append(pod.InitContainers, corev1.Container{
		Name: nookkuPythonContainer, Command: []string{"sh", "-c", nookkuPythonScript},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	})
	syncNookkuPythonCheck(pod, pod.Containers[0].Name)
}

// syncNookkuPythonCheck gives the Python check the image, the pull policy, the literal PATH and the
// user and group of the main container. Thus the check finds the python3 that the harness finds. The check runs in
// the folder "/", because the working folder of the main container can be on a volume that the
// check does not mount. The check keeps the restrictions of a helper: no privilege escalation, no
// capabilities and a read-only root file system. withPodTemplate calls it again after the merge, because spec.podTemplate can change
// the image and the env of the main container. A pod with no check does not change.
func syncNookkuPythonCheck(pod *corev1.PodSpec, main string) {
	i := slices.IndexFunc(pod.InitContainers, func(c corev1.Container) bool { return c.Name == nookkuPythonContainer })
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
	check.SecurityContext = sc
	if p := slices.IndexFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == pathEnv }); p >= 0 && c.Env[p].ValueFrom == nil {
		check.Env = []corev1.EnvVar{{Name: pathEnv, Value: c.Env[p].Value}}
	}
}
