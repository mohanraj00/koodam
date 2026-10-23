//go:build e2e
// +build e2e

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

package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// ptProfile binds ptNS. The specs make their own namespace, so they do not change the
	// Workspace specs.
	ptProfile = "koodam-e2e-pt"
	ptNS      = "koodam-e2e-pt"
	// ptSecret is a user Secret. The value is not a credential.
	ptSecret = "e2e-user-config"
)

var _ = Describe("Pod template", Ordered, Label("workspace"), func() {
	workspace := func(name string, t *corev1alpha1.PodTemplate) *corev1alpha1.Workspace {
		return &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ptNS},
			Spec: corev1alpha1.WorkspaceSpec{Image: workspaceImage, Storage: resource.MustParse("1Gi"),
				PodTemplate: t}}
	}
	envFrom := func(secret string) []corev1.EnvVar {
		return []corev1.EnvVar{{Name: "E2E_CONFIG", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "config"}}}}
	}

	BeforeAll(func() {
		DeferCleanup(func() {
			_ = k8s.DeleteAllOf(ctx, &corev1alpha1.Workspace{}, client.InNamespace(ptNS))
			Eventually(func() int {
				l := &corev1alpha1.WorkspaceList{}
				_ = k8s.List(ctx, l, client.InNamespace(ptNS))
				return len(l.Items)
			}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept a Workspace.")
			_ = k8s.Delete(ctx, &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: ptProfile}})
			_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ptNS}})
			Eventually(func() bool {
				return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Name: ptNS}, &corev1.Namespace{}))
			}, 5*time.Minute, 2*time.Second).Should(BeTrue(), "The namespace "+ptNS+" was not deleted.")
		})
		for _, o := range []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ptNS,
				Labels: map[string]string{corev1alpha1.ProfileLabel: ptProfile}}},
			&corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: ptProfile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{ptNS}}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ptSecret, Namespace: ptNS},
				StringData: map[string]string{"config": "e2e"}},
		} {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}
	})

	AfterEach(func() { dumpWorkspaces() })

	It("merges spec.podTemplate into the Workspace pod", func() {
		ws := workspace("merged", &corev1alpha1.PodTemplate{
			Metadata: corev1alpha1.PodTemplateMetadata{Labels: map[string]string{"group": "e2e"}},
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
				Containers: []corev1.Container{{Name: "workspace", Env: envFrom(ptSecret),
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1Gi")}}}},
			}})
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		waitPhase(ws, corev1alpha1.WorkspaceRunning)

		pod := &corev1.Pod{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: ptNS, Name: ws.Name + "-0"}, pod)).To(Succeed())
		Expect(pod.Labels).To(HaveKeyWithValue("group", "e2e"))
		Expect(pod.Labels).To(HaveKeyWithValue(wsLabel, ws.Name))
		Expect(pod.Spec.NodeSelector).To(HaveKeyWithValue("kubernetes.io/os", "linux"))
		main := pod.Spec.Containers[0]
		Expect(main.Name).To(Equal("workspace"))
		Expect(main.Resources.Limits.Memory().String()).To(Equal("1Gi"))
		Expect(main.Env).To(ContainElement(HaveField("Name", "E2E_CONFIG")))
		Expect(main.SecurityContext).NotTo(BeNil())
		Expect(main.SecurityContext.RunAsNonRoot).To(HaveValue(BeTrue()), "Koodam keeps the container security.")
	})

	It("refuses hostNetwork in the API server", func() {
		ws := workspace("host", &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{HostNetwork: true}})
		err := k8s.Create(ctx, ws)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "The API server accepted hostNetwork.")
	})

	It("refuses a reference to a Secret that Koodam owns, and makes no pod", func() {
		// merged-ssh-host is the host key Secret of the Workspace of the first spec. The unit tests
		// cover the kd- prefix. This spec checks the metadata read against a real API server.
		owned := client.ObjectKey{Namespace: ptNS, Name: "merged-ssh-host"}
		Eventually(func() error { return k8s.Get(ctx, owned, &corev1.Secret{}) }, time.Minute, time.Second).
			Should(Succeed(), "The first spec makes the Workspace that owns the Secret.")
		ws := workspace("refused", &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "workspace", Env: envFrom("merged-ssh-host")}},
		}})
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		waitPhase(ws, corev1alpha1.WorkspaceFailed)
		c := meta.FindStatusCondition(ws.Status.Conditions, "Ready")
		Expect(c).NotTo(BeNil())
		Expect(c.Reason).To(Equal("InvalidPodTemplate"))
		Expect(c.Message).To(ContainSubstring("spec.podTemplate.spec.containers[0].env[0].valueFrom.secretKeyRef.name"))
		err := k8s.Get(ctx, client.ObjectKey{Namespace: ptNS, Name: ws.Name}, &appsv1.StatefulSet{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "The controller made a StatefulSet.")
	})
})
