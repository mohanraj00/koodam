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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Workspace harnesses", func() {
	const installer = "example.com/koodam:1"
	ws := func(h ...corev1alpha1.HarnessSpec) *corev1alpha1.Workspace {
		return &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "h", Namespace: "n"},
			Spec: corev1alpha1.WorkspaceSpec{Harnesses: h}}
	}
	pod := func(w *corev1alpha1.Workspace, image string) corev1.PodSpec {
		r := &WorkspaceReconciler{Image: "example.com/ws:1", InstallerImage: image}
		return r.statefulSet(w, true).Spec.Template.Spec
	}

	It("adds one checked install for each harness and mounts the folder read-only", func() {
		p := pod(ws(corev1alpha1.HarnessSpec{Name: "claude-code", Version: "2.1.0", Libc: "musl"},
			corev1alpha1.HarnessSpec{Name: "codex"}), installer)

		Expect(p.InitContainers).To(HaveLen(2))
		Expect(p.InitContainers[0].Name).To(Equal("harness-claude-code"))
		Expect(p.InitContainers[0].Command).To(Equal([]string{"/harness-install", "-harness", "claude-code",
			"-version", "2.1.0", "-libc", "musl", "-dir", harnessDir}))
		Expect(p.InitContainers[1].Name).To(Equal("harness-codex"))
		Expect(p.InitContainers[1].Command).To(ContainElements("-libc", "glibc"))
		for _, c := range p.InitContainers {
			Expect(c.Image).To(Equal(installer))
			Expect(c.Env).To(BeEmpty())
			Expect(c.EnvFrom).To(BeEmpty())
			Expect(c.SecurityContext.RunAsNonRoot).To(HaveValue(BeTrue()))
			Expect(c.SecurityContext.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
			Expect(c.SecurityContext.Capabilities.Drop).To(Equal([]corev1.Capability{"ALL"}))
			Expect(c.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		}
		Expect(p.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: "harness", MountPath: harnessDir, ReadOnly: true}))
		Expect(p.Volumes).To(ContainElement(HaveField("Name", "harness")))
	})

	It("does not change the pod with no harness or no installer image", func() {
		want := pod(ws(), installer)
		Expect(want.InitContainers).To(BeEmpty())
		Expect(pod(ws(corev1alpha1.HarnessSpec{Name: "codex"}), "")).To(Equal(want))
	})
})
