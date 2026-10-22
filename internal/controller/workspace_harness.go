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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// condHarnesses tells if the pod installs the harnesses of spec.harnesses.
const condHarnesses = "Harnesses"

// harnessCondition is True if spec.harnesses is empty or the controller has an installer image.
// With no installer image, addWorkspaceHarnesses adds no harness, and the condition says so.
func harnessCondition(ws *corev1alpha1.Workspace, image string) metav1.Condition {
	c := metav1.Condition{Type: condHarnesses, Status: metav1.ConditionTrue, Reason: "Installed",
		Message: "The pod installs each harness of spec.harnesses.", ObservedGeneration: ws.Generation}
	switch {
	case len(ws.Spec.Harnesses) == 0:
		c.Reason, c.Message = "None", "spec.harnesses is empty."
	case image == "":
		c.Status, c.Reason = metav1.ConditionFalse, "NoInstallerImage"
		c.Message = "The controller has no --installer-image, so the pod has no harness."
	}
	return c
}

// addWorkspaceHarnesses adds one init container for each harness of the Workspace. Each init
// container downloads its harness binary into the same emptyDir and checks its sha256, as for an
// Agent run. The workspace container mounts the emptyDir read-only at harnessDir. The login
// PATH of the image has harnessDir. The init containers have no credential env.
// If there is no harness or no installer image, the pod does not change.
func addWorkspaceHarnesses(pod *corev1.PodSpec, harnesses []corev1alpha1.HarnessSpec, image string) {
	if len(harnesses) == 0 || image == "" {
		return
	}
	const vol = "harness"
	limit := resource.MustParse("2Gi")
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: vol, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}})
	for _, h := range harnesses {
		libc := h.Libc
		if libc == "" {
			libc = "glibc"
		}
		pod.InitContainers = append(pod.InitContainers,
			installerContainer(vol+"-"+h.Name, vol, h.Name, h.Version, libc, image))
	}
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: harnessDir, ReadOnly: true})
}

// installerContainer is an init container that runs /harness-install for one harness. It writes
// the binary into the volume vol at harnessDir. It has no credential env.
func installerContainer(name, vol, harness, version, libc, image string) corev1.Container {
	return corev1.Container{
		Name: name, Image: image,
		Command: []string{"/harness-install", "-harness", harness, "-version", version, "-libc", libc,
			"-dir", harnessDir},
		VolumeMounts: []corev1.VolumeMount{{Name: vol, MountPath: harnessDir}},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(int64(65532)), AllowPrivilegeEscalation: new(false),
			ReadOnlyRootFilesystem: new(true),
			Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}
