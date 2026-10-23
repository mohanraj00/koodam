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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// lrProfile binds lrNS. The LimitRange of lrNS has no effect on the other specs.
	lrProfile = "koodam-e2e-lr"
	lrNS      = "koodam-e2e-lr"
)

var _ = Describe("Namespace resource limits", Ordered, Label("workspace"), func() {
	memory := func(q string) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(q)}
	}
	workspace := func(name, limit string) *corev1alpha1.Workspace {
		ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: lrNS},
			Spec: corev1alpha1.WorkspaceSpec{Image: workspaceImage, Storage: resource.MustParse("1Gi")}}
		if limit != "" {
			ws.Spec.PodTemplate = &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "workspace", Resources: corev1.ResourceRequirements{Limits: memory(limit)}}}}}
		}
		return ws
	}
	// mainResources returns the resources of the workspace container. The init containers must
	// have the default, because the pod template sets no value for them.
	mainResources := func(ws *corev1alpha1.Workspace) corev1.ResourceRequirements {
		pod := &corev1.Pod{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: lrNS, Name: ws.Name + "-0"}, pod)).To(Succeed())
		for _, c := range pod.Spec.InitContainers {
			Expect(c.Resources.Limits.Memory().String()).To(Equal("1Gi"), "The init container "+c.Name+" does not have the default.")
		}
		Expect(pod.Spec.Containers).NotTo(BeEmpty())
		Expect(pod.Spec.Containers[0].Name).To(Equal("workspace"))
		return pod.Spec.Containers[0].Resources
	}

	BeforeAll(func() {
		DeferCleanup(func() {
			_ = k8s.DeleteAllOf(ctx, &corev1alpha1.Workspace{}, client.InNamespace(lrNS))
			Eventually(func() int {
				l := &corev1alpha1.WorkspaceList{}
				if err := k8s.List(ctx, l, client.InNamespace(lrNS)); err != nil {
					return -1
				}
				return len(l.Items)
			}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept a Workspace.")
			_ = k8s.Delete(ctx, &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: lrProfile}})
			_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: lrNS}})
			Eventually(func() bool {
				return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Name: lrNS}, &corev1.Namespace{}))
			}, 5*time.Minute, 2*time.Second).Should(BeTrue(), "The namespace "+lrNS+" was not deleted.")
		})
		for _, o := range []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: lrNS,
				Labels: map[string]string{corev1alpha1.ProfileLabel: lrProfile}}},
			&corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: lrProfile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{lrNS}}},
			&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: lrNS},
				Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
					Type:           corev1.LimitTypeContainer,
					Default:        memory("1Gi"),
					DefaultRequest: memory("256Mi"),
					Max:            memory("2Gi"),
				}}}},
		} {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}
	})

	AfterEach(func() { dumpWorkspaces() })

	It("gives the default to a Workspace with no value, and keeps a value under the maximum", func() {
		plain, set := workspace("plain", ""), workspace("set", "1536Mi")
		Expect(k8s.Create(ctx, plain)).To(Succeed())
		Expect(k8s.Create(ctx, set)).To(Succeed())
		waitPhase(plain, corev1alpha1.WorkspaceRunning)
		waitPhase(set, corev1alpha1.WorkspaceRunning)
		r := mainResources(plain)
		Expect(r.Limits.Memory().String()).To(Equal("1Gi"), "The pod does not have the LimitRange default.")
		Expect(r.Requests.Memory().String()).To(Equal("256Mi"), "The pod does not have the LimitRange default request.")
		r = mainResources(set)
		Expect(r.Limits.Memory().String()).To(Equal("1536Mi"), "The pod does not have the value of the pod template.")
		// The next spec does not need these pods. Delete them to release the node memory.
		Expect(k8s.Delete(ctx, plain)).To(Succeed())
		Expect(k8s.Delete(ctx, set)).To(Succeed())
	})

	It("makes no pod for a value above the maximum", func() {
		ws := workspace("large", "4Gi")
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		// The API server refuses the pod, not the Workspace. The StatefulSet records the refusal.
		Eventually(func() []string {
			l := &corev1.EventList{}
			_ = k8s.List(ctx, l, client.InNamespace(lrNS))
			var msgs []string
			for _, e := range l.Items {
				if e.InvolvedObject.Kind == "StatefulSet" && e.InvolvedObject.Name == ws.Name && e.Reason == "FailedCreate" {
					msgs = append(msgs, e.Message)
				}
			}
			return msgs
		}, 2*time.Minute, 2*time.Second).Should(ContainElement(ContainSubstring("maximum memory usage")))
		waitPhase(ws, corev1alpha1.WorkspacePending)
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: lrNS, Name: ws.Name}, &appsv1.StatefulSet{})).To(Succeed())
		err := k8s.Get(ctx, client.ObjectKey{Namespace: lrNS, Name: ws.Name + "-0"}, &corev1.Pod{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "The API server made a pod above the maximum.")
	})
})
