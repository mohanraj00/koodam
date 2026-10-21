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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Profile Controller", func() {
	ctx := context.Background()

	// makeNamespace makes a namespace. If profile is not empty, the namespace gets the Profile label.
	makeNamespace := func(name, profile string) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if profile != "" {
			ns.Labels = map[string]string{corev1alpha1.ProfileLabel: profile}
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	}

	// reconcileProfile runs one reconcile and returns the Profile after it.
	reconcileProfile := func(name string) *corev1alpha1.Profile {
		r := &ProfileReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		key := types.NamespacedName{Name: name}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		p := &corev1alpha1.Profile{}
		Expect(k8sClient.Get(ctx, key, p)).To(Succeed())
		return p
	}

	It("binds only the namespaces that the Profile lists and that have its label", func() {
		makeNamespace("pf-listed-labelled", "pf-alice")
		makeNamespace("pf-labelled-only", "pf-alice")
		makeNamespace("pf-listed-only", "")
		makeNamespace("pf-other-label", "pf-bob")

		p := &corev1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: "pf-alice"},
			Spec: corev1alpha1.ProfileSpec{Namespaces: []string{
				"pf-listed-labelled", "pf-listed-only", "pf-other-label", "pf-missing",
			}},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })

		got := reconcileProfile("pf-alice")
		By("binding the namespace that is listed and labelled")
		Expect(got.Status.BoundNamespaces).To(ConsistOf("pf-listed-labelled"))
		By("not binding the labelled namespace that is not listed")
		Expect(got.Status.BoundNamespaces).NotTo(ContainElement("pf-labelled-only"))
		By("not binding the listed namespace without the label")
		Expect(got.Status.BoundNamespaces).NotTo(ContainElement("pf-listed-only"))
		By("not binding the namespace with the label of another Profile")
		Expect(got.Status.BoundNamespaces).NotTo(ContainElement("pf-other-label"))

		ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.ObservedGeneration).To(Equal(got.Generation))
	})

	It("updates the bound namespaces when a namespace label changes", func() {
		makeNamespace("pf-change", "")
		p := &corev1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: "pf-carol"},
			Spec:       corev1alpha1.ProfileSpec{Namespaces: []string{"pf-change"}},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })
		Expect(reconcileProfile("pf-carol").Status.BoundNamespaces).To(BeEmpty())

		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "pf-change"}, ns)).To(Succeed())
		old := ns.DeepCopy()
		ns.Labels = map[string]string{corev1alpha1.ProfileLabel: "pf-carol"}
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())

		By("mapping the namespace event to the Profile in the label")
		Expect(profileForNamespace(ctx, ns)).To(ConsistOf(
			reconcile.Request{NamespacedName: types.NamespacedName{Name: "pf-carol"}}))
		Expect(profileForNamespace(ctx, old)).To(BeEmpty())
		Expect(reconcileProfile("pf-carol").Status.BoundNamespaces).To(ConsistOf("pf-change"))

		By("removing the binding when the label goes to another Profile")
		ns.Labels[corev1alpha1.ProfileLabel] = "pf-dave"
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		Expect(reconcileProfile("pf-carol").Status.BoundNamespaces).To(BeEmpty())
	})

	It("refuses a namespace name that is not a DNS-1123 label", func() {
		p := &corev1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: "pf-bad"},
			Spec:       corev1alpha1.ProfileSpec{Namespaces: []string{"Not_A_Label"}},
		}
		Expect(k8sClient.Create(ctx, p)).NotTo(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, p))).To(Succeed())
	})
})
