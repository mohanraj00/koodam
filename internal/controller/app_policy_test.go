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
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// These specs apply config/apps/admission/policy.yaml to the envtest API server. They run no
// controller. The key hash is a fixture, not the hash of a key.
var _ = Describe("AppVersion admission policy", Ordered, func() {
	const ns = "default"
	ctx := context.Background()
	newVersion := func(name string, change func(*corev1alpha1.AppVersion)) *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: "policy-app"}, BudgetPerDay: resource.MustParse("5"),
				Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: "registry.example.com/policy:v1",
						Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/ok", Port: intstr.FromString("web")}}},
					}}}}},
			}}
		if change != nil {
			change(av)
		}
		return av
	}
	create := func(av *corev1alpha1.AppVersion) error {
		err := k8sClient.Create(ctx, av)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, av))).To(Succeed()) })
		}
		return err
	}
	port := func(av *corev1alpha1.AppVersion) *corev1.ContainerPort {
		return &av.Spec.Template.PodTemplate.Spec.Containers[0].Ports[0]
	}
	refused := func(err error, text string) {
		GinkgoHelper()
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}

	BeforeAll(func() {
		src, err := os.ReadFile(filepath.Join("..", "..", "config", "apps", "admission", "policy.yaml"))
		Expect(err).NotTo(HaveOccurred())
		for doc := range bytes.SplitSeq(src, []byte("\n---\n")) {
			u := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(doc, &u.Object)).To(Succeed())
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, u))).To(Succeed()) })
		}
		// The API server starts to enforce a new policy after a short time.
		Eventually(func() error {
			err := k8sClient.Create(ctx, newVersion("policy-probe", func(av *corev1alpha1.AppVersion) { port(av).Name = "" }))
			if err == nil {
				_ = k8sClient.Delete(ctx, newVersion("policy-probe", nil))
			}
			return err
		}, time.Minute, time.Second).ShouldNot(Succeed())
	})

	It("refuses a create with a name or a port that the controller cannot use", func() {
		const portText = "Each port of the app container"
		refused(create(newVersion("policy-a", func(av *corev1alpha1.AppVersion) { port(av).Name = "" })), portText)
		refused(create(newVersion("policy-a", func(av *corev1alpha1.AppVersion) { port(av).Protocol = corev1.ProtocolUDP })), portText)
		refused(create(newVersion("policy-a", func(av *corev1alpha1.AppVersion) { port(av).HostPort = 8080 })), portText)
		long := "policy-" + string(bytes.Repeat([]byte("a"), 54))
		refused(create(newVersion(long, nil)), "60 characters")
		refused(create(newVersion("policy.a", nil)), "DNS label")
		refused(create(newVersion("policy-a", func(av *corev1alpha1.AppVersion) {
			av.Spec.AppRef.Name = string(bytes.Repeat([]byte("a"), 64))
		})), "spec.appRef.name")
		Expect(create(newVersion("policy-a", nil))).To(Succeed())
	})

	It("accepts a template change before the key, and refuses it after the key", func() {
		av := newVersion("policy-b", nil)
		Expect(create(av)).To(Succeed())
		// The version has no key. A person can correct the template, but not to a bad port.
		av.Spec.Template.Model = "model-b"
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		port(av).Name = ""
		refused(k8sClient.Update(ctx, av), "Each port of the app container")

		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: av.Name}, av)).To(Succeed())
		av.Status.KeyHash = "fixture-hash"
		Expect(k8sClient.Status().Update(ctx, av)).To(Succeed())

		av.Spec.Template.Model = "model-c"
		refused(k8sClient.Update(ctx, av), "spec.template is immutable")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: av.Name}, av)).To(Succeed())
		av.Spec.Template.PodTemplate.Spec.Containers[0].Image = "registry.example.com/app:v2"
		refused(k8sClient.Update(ctx, av), "spec.template is immutable")

		// The fields outside the template can change, and the controller can write its finalizer.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: av.Name}, av)).To(Succeed())
		av.Spec.Replicas = new(int32(3))
		av.Spec.BudgetPerDay = resource.MustParse("7")
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		av.Finalizers = []string{appFinalizer}
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		av.Finalizers = nil
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
	})
})
