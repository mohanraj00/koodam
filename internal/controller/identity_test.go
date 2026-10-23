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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("addIdentity", func() {
	It("adds the run and the user labels and keeps the other labels", func() {
		m := metav1.ObjectMeta{Labels: map[string]string{agentLabel: "a"}}
		addIdentity(&m, "kd-a", "dev-team")
		Expect(m.Labels).To(Equal(map[string]string{agentLabel: "a", RunLabel: "kd-a", UserLabel: "dev-team"}))
	})

	It("does not change a map that a selector shares", func() {
		shared := map[string]string{WorkspaceLabel: "ws"}
		m := metav1.ObjectMeta{Labels: shared}
		addIdentity(&m, "", "dev-team")
		Expect(shared).To(Equal(map[string]string{WorkspaceLabel: "ws"}))
		Expect(m.Labels).To(HaveKeyWithValue(UserLabel, "dev-team"))
	})

	DescribeTable("adds no label for an empty, a long or an invalid value",
		func(user string) {
			m := metav1.ObjectMeta{}
			addIdentity(&m, "", user)
			Expect(m.Labels).To(BeEmpty())
		},
		Entry("empty", ""),
		Entry("64 characters", strings.Repeat("a", 64)),
		Entry("a character that a label value cannot have", "alice@example"),
	)
})

var _ = Describe("boundProfile", func() {
	It("returns the Profile name only if the Profile binds the namespace", func() {
		ctx := context.Background()
		const name = "identity-bound"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name,
			Labels: map[string]string{corev1alpha1.ProfileLabel: name}}}
		p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() {
			for _, o := range []client.Object{p, ns} {
				Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), o))).To(Succeed())
			}
		})
		Expect(boundProfile(ctx, k8sClient, name)).To(BeEmpty(), "the Profile does not list the namespace")

		p.Spec.Namespaces = []string{name}
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Expect(boundProfile(ctx, k8sClient, name)).To(Equal(name))
		Expect(boundProfile(ctx, k8sClient, "identity-missing")).To(BeEmpty())
	})
})
