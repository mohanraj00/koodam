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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// testKeyTestID is the Nookku test ID in the TestKey tests.
const testKeyTestID = "login-smoke"

// testKeyNamePrefix is the GenerateName of the TestKeys in the policy tests.
const testKeyNamePrefix = "testkey-request-"

var _ = Describe("TestKey controller", func() {
	DescribeTable("refuses a key before it reaches the gateway",
		func(change func(*corev1alpha1.Workspace, *corev1alpha1.TestKey), reason string) {
			ctx := context.Background()
			workspace := &corev1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "testkey-workspace-", Namespace: metav1.NamespaceDefault},
				Spec: corev1alpha1.WorkspaceSpec{
					Gateway: &corev1alpha1.WorkspaceGateway{
						Model: "local-model", BudgetPerDay: resource.MustParse("5"),
					},
					TestKeys: &corev1alpha1.WorkspaceTestKeys{
						MaxBudget: resource.MustParse("1"), MaxTTL: metav1.Duration{Duration: time.Hour},
					},
				},
			}
			testKey := &corev1alpha1.TestKey{
				ObjectMeta: metav1.ObjectMeta{GenerateName: testKeyNamePrefix, Namespace: workspace.Namespace},
				Spec: corev1alpha1.TestKeySpec{
					TestID: testKeyTestID, Budget: resource.MustParse("0.5"),
					TTL: metav1.Duration{Duration: 30 * time.Minute},
				},
			}
			change(workspace, testKey)
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())
			DeferCleanup(k8sClient.Delete, ctx, workspace)

			testKey.Spec.WorkspaceRef.Name = workspace.Name
			if testKey.Spec.WorkspaceRef.UID == "" {
				testKey.Spec.WorkspaceRef.UID = workspace.UID
			}
			Expect(k8sClient.Create(ctx, testKey)).To(Succeed())
			DeferCleanup(k8sClient.Delete, ctx, testKey)

			gw := &wsGateway{keys: map[string]bool{}}
			controller := &TestKeyReconciler{Client: k8sClient, APIReader: k8sClient, Gateway: gw,
				SingleInstanceGateway: true}
			key := client.ObjectKeyFromObject(testKey)
			_, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, testKey)).To(Succeed())

			condition := meta.FindStatusCondition(testKey.Status.Conditions, "Active")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reason))
			Expect(testKey.Status.KeyAlias).To(BeEmpty())
			Expect(gw.order).To(BeEmpty())
		},
		Entry("an earlier Workspace with the same name", func(_ *corev1alpha1.Workspace, key *corev1alpha1.TestKey) {
			key.Spec.WorkspaceRef.UID = "earlier-workspace"
		}, "WorkspaceUIDMismatch"),
		Entry("a suspended Workspace", func(workspace *corev1alpha1.Workspace, _ *corev1alpha1.TestKey) {
			workspace.Spec.Suspend = true
		}, "WorkspaceSuspended"),
		Entry("a Workspace without opt-in", func(workspace *corev1alpha1.Workspace, _ *corev1alpha1.TestKey) {
			workspace.Spec.TestKeys = nil
		}, "TestKeysDisabled"),
		Entry("a budget above the Workspace limit", func(_ *corev1alpha1.Workspace, key *corev1alpha1.TestKey) {
			key.Spec.Budget = resource.MustParse("1.01")
		}, "BudgetLimitExceeded"),
		Entry("a TTL above the Workspace limit", func(_ *corev1alpha1.Workspace, key *corev1alpha1.TestKey) {
			key.Spec.TTL.Duration = time.Hour + time.Second
		}, "TTLLimitExceeded"),
	)

	It("removes an old refusal when the Workspace permits the key again", func() {
		ctx := context.Background()
		workspace := &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "testkey-workspace-", Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.WorkspaceSpec{
				Suspend: true,
				TestKeys: &corev1alpha1.WorkspaceTestKeys{
					MaxBudget: resource.MustParse("1"), MaxTTL: metav1.Duration{Duration: time.Hour},
				},
			},
		}
		Expect(k8sClient.Create(ctx, workspace)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ctx, workspace)
		testKey := &corev1alpha1.TestKey{
			ObjectMeta: metav1.ObjectMeta{GenerateName: testKeyNamePrefix, Namespace: workspace.Namespace},
			Spec: corev1alpha1.TestKeySpec{
				WorkspaceRef: corev1alpha1.TestKeyWorkspaceRef{Name: workspace.Name, UID: workspace.UID},
				TestID:       testKeyTestID, Budget: resource.MustParse("0.5"),
				TTL: metav1.Duration{Duration: 30 * time.Minute},
			},
		}
		Expect(k8sClient.Create(ctx, testKey)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ctx, testKey)

		controller := &TestKeyReconciler{Client: k8sClient, APIReader: k8sClient, Gateway: &wsGateway{keys: map[string]bool{}},
			SingleInstanceGateway: true}
		key := client.ObjectKeyFromObject(testKey)
		reason := func() string {
			_, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, testKey)).To(Succeed())
			if c := meta.FindStatusCondition(testKey.Status.Conditions, "Active"); c != nil {
				return c.Reason
			}
			return ""
		}
		Expect(reason()).To(Equal("WorkspaceSuspended"))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workspace), workspace)).To(Succeed())
		workspace.Spec.Suspend = false
		Expect(k8sClient.Update(ctx, workspace)).To(Succeed())
		Expect(reason()).To(BeEmpty())
		Expect(meta.FindStatusCondition(testKey.Status.Conditions, "Active")).To(BeNil())
	})

	It("refuses a key for a Workspace that does not exist", func() {
		ctx := context.Background()
		testKey := &corev1alpha1.TestKey{
			ObjectMeta: metav1.ObjectMeta{GenerateName: testKeyNamePrefix, Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.TestKeySpec{
				WorkspaceRef: corev1alpha1.TestKeyWorkspaceRef{Name: "no-such-workspace", UID: "no-such-uid"},
				TestID:       testKeyTestID, Budget: resource.MustParse("0.5"),
				TTL: metav1.Duration{Duration: 30 * time.Minute},
			},
		}
		Expect(k8sClient.Create(ctx, testKey)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ctx, testKey)

		controller := &TestKeyReconciler{Client: k8sClient, APIReader: k8sClient}
		key := client.ObjectKeyFromObject(testKey)
		_, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, testKey)).To(Succeed())
		condition := meta.FindStatusCondition(testKey.Status.Conditions, "Active")
		Expect(condition).NotTo(BeNil())
		Expect(condition.Reason).To(Equal("WorkspaceNotFound"))
	})
})
