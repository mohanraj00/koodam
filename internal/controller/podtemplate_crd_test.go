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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The CRD validation of spec.podTemplate. These tests only send objects to the API server.
var _ = Describe("Pod template CRD", func() {
	ctx := context.Background()
	const runtimeClass, agentMain = "gvisor", "agent"

	create := func(o client.Object) error {
		err := k8sClient.Create(ctx, o)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed()) })
		}
		return err
	}
	workspace := func(name string, t *corev1alpha1.PodTemplate) *corev1alpha1.Workspace {
		return &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.WorkspaceSpec{PodTemplate: t}}
	}
	agent := func(name string, t *corev1alpha1.PodTemplate) *corev1alpha1.Agent {
		return &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.AgentSpec{Image: "img", Command: []string{"run"}, Model: "m",
				Budget: resource.MustParse("1"), PodTemplate: t}}
	}
	expectInvalid := func(err error, text string) {
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}

	It("accepts a template with no containers", func() {
		t := &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{RuntimeClassName: new(runtimeClass),
			NodeSelector: map[string]string{"pool": "agents"}}}
		Expect(create(workspace("pt-no-containers", t))).To(Succeed())
		Expect(create(agent("pt-no-containers", t))).To(Succeed())
	})

	It("accepts resources on the main container and a label", func() {
		t := &corev1alpha1.PodTemplate{Metadata: corev1alpha1.PodTemplateMetadata{Labels: map[string]string{"team": "a"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: agentMain,
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}}}}}
		Expect(create(agent("pt-resources", t))).To(Succeed())
	})

	DescribeTable("refuses a pod field that Koodam owns or refuses",
		func(name, text string, change func(*corev1.PodSpec)) {
			t := &corev1alpha1.PodTemplate{}
			change(&t.Spec)
			expectInvalid(create(workspace(name, t)), text)
		},
		Entry(nil, "pt-host-network", "host network", func(s *corev1.PodSpec) { s.HostNetwork = true }),
		Entry(nil, "pt-host-pid", "host network", func(s *corev1.PodSpec) { s.HostPID = true }),
		Entry(nil, "pt-host-users", "host network", func(s *corev1.PodSpec) { s.HostUsers = new(true) }),
		Entry(nil, "pt-share", "host network", func(s *corev1.PodSpec) { s.ShareProcessNamespace = new(true) }),
		Entry(nil, "pt-sa", "Koodam owns", func(s *corev1.PodSpec) { s.ServiceAccountName = "admin" }),
		Entry(nil, "pt-token", "Koodam owns", func(s *corev1.PodSpec) { s.AutomountServiceAccountToken = new(true) }),
		Entry(nil, "pt-sec", "Koodam owns", func(s *corev1.PodSpec) { s.SecurityContext = &corev1.PodSecurityContext{} }),
		Entry(nil, "pt-restart", "Koodam owns", func(s *corev1.PodSpec) { s.RestartPolicy = corev1.RestartPolicyAlways }),
	)

	It("accepts hostUsers false", func() {
		t := &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{HostUsers: new(false)}}
		Expect(create(workspace("pt-host-users-false", t))).To(Succeed())
	})

	It("refuses a label or an annotation with the prefix koodam.dev/", func() {
		t := &corev1alpha1.PodTemplate{Metadata: corev1alpha1.PodTemplateMetadata{
			Labels: map[string]string{"koodam.dev/user": "someone"}}}
		expectInvalid(create(workspace("pt-label", t)), "reserved")
		t = &corev1alpha1.PodTemplate{Metadata: corev1alpha1.PodTemplateMetadata{
			Annotations: map[string]string{"koodam.dev/x": "y"}}}
		expectInvalid(create(workspace("pt-annotation", t)), "reserved")
	})

	It("refuses an image in the template and in spec.image of a Workspace", func() {
		ws := workspace("pt-ws-image", &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Image: "other"}}}})
		Expect(create(ws)).To(Succeed(), "the template alone can set the image")
		ws = workspace("pt-ws-image-2", ws.Spec.PodTemplate)
		ws.Spec.Image = "img"
		expectInvalid(create(ws), "spec.image or in the pod template")
	})

	It("refuses an image or a command on the main container of an Agent", func() {
		for name, c := range map[string]corev1.Container{
			"pt-agent-image":   {Name: agentMain, Image: "other"},
			"pt-agent-command": {Name: agentMain, Command: []string{"sh"}},
		} {
			expectInvalid(create(agent(name, &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
				Containers: []corev1.Container{c}}})), "spec.image and spec.command")
		}
		Expect(create(agent("pt-agent-sidecar", &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sidecar", Image: "proxy"}}}}))).To(Succeed())
	})
})
