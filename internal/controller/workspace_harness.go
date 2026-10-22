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

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

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
		pod.InitContainers = append(pod.InitContainers, corev1.Container{
			Name: vol + "-" + h.Name, Image: image,
			Command: []string{"/harness-install", "-harness", h.Name, "-version", h.Version, "-libc", libc,
				"-dir", harnessDir},
			VolumeMounts: []corev1.VolumeMount{{Name: vol, MountPath: harnessDir}},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot: new(true), RunAsUser: new(int64(65532)), AllowPrivilegeEscalation: new(false),
				ReadOnlyRootFilesystem: new(true),
				Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		})
	}
	c := &pod.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: vol, MountPath: harnessDir, ReadOnly: true})
}
