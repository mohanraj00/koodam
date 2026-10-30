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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The CRD validation of the TestKey and of the Workspace test key limits. These tests do not run
// a controller. They only send objects to the API server. The budgets are small numbers and the
// objects hold no key value.
var _ = Describe("TestKey CRD", func() {
	ctx := context.Background()
	ns := metav1.NamespaceDefault
	// These are the schema field names and the fixture values that the tests use more than one time.
	const (
		apiVersionField = "apiVersion"
		kindField       = "kind"
		metadataField   = "metadata"
		nameField       = "name"
		namespaceField  = "namespace"
		specField       = "spec"
		uidField        = "uid"
		budgetField     = "budget"
		ttlField        = "ttl"
		maxBudgetField  = "maxBudget"
		maxTTLField     = "maxTTL"
		workspaceKind   = "Workspace"
		workspaceName   = "dev"
		workspaceUID    = "ws-uid-1"
		otherName       = "other"
	)

	newTestKey := func(name string) *corev1alpha1.TestKey {
		return &corev1alpha1.TestKey{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.TestKeySpec{
				WorkspaceRef: corev1alpha1.TestKeyWorkspaceRef{Name: workspaceName, UID: workspaceUID},
				TestID:       "login-smoke",
				Budget:       resource.MustParse("0.5"),
				TTL:          metav1.Duration{Duration: 30 * time.Minute},
			},
		}
	}

	// rawTestKey is a valid TestKey as a map, so that a test can remove a field.
	rawTestKey := func(name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			apiVersionField: corev1alpha1.GroupVersion.String(),
			kindField:       "TestKey",
			metadataField:   map[string]any{nameField: name, namespaceField: ns},
			specField: map[string]any{
				"workspaceRef": map[string]any{nameField: workspaceName, uidField: workspaceUID},
				"testID":       "login-smoke",
				budgetField:    "0.5",
				ttlField:       "30m",
			},
		}}
		return u
	}

	create := func(obj client.Object) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
		}
		return err
	}

	expectInvalid := func(err error) {
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
	}

	jsonPatch := func(obj client.Object, patch string) error {
		return k8sClient.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, []byte(patch)))
	}

	It("accepts a valid TestKey", func() {
		Expect(create(newTestKey("tk-valid"))).To(Succeed())
	})

	// The rule is the Nookku TEST_ID rule. The bridge makes IDs like "20261030-101900-a1b2", but
	// the other forms are valid too.
	It("accepts each valid Nookku test ID", func() {
		for i, id := range []string{"20261030-101900-a1b2", "a", "0", "login_smoke-", "login-smoke_",
			strings.Repeat("t", 300)} {
			u := rawTestKey("tk-id-" + string(rune('a'+i)))
			Expect(unstructured.SetNestedField(u.Object, id, specField, "testID")).To(Succeed())
			Expect(create(u)).To(Succeed(), "test ID %q", id)
		}
	})

	It("accepts budgets in each Quantity form", func() {
		for i, b := range []any{"2", int64(3), "500m", "1e-2", "0.01", "1k"} {
			u := rawTestKey("tk-qty-" + string(rune('a'+i)))
			Expect(unstructured.SetNestedField(u.Object, b, specField, budgetField)).To(Succeed())
			Expect(create(u)).To(Succeed(), "budget %v", b)
		}
	})

	It("refuses a TestKey without a spec", func() {
		u := rawTestKey("tk-no-spec")
		unstructured.RemoveNestedField(u.Object, specField)
		expectInvalid(create(u))
	})

	DescribeTable("refuses a TestKey without a necessary field",
		func(path ...string) {
			u := rawTestKey("tk-missing")
			unstructured.RemoveNestedField(u.Object, append([]string{specField}, path...)...)
			expectInvalid(create(u))
		},
		Entry("workspaceRef", "workspaceRef"),
		Entry("workspaceRef.name", "workspaceRef", "name"),
		Entry("workspaceRef.uid", "workspaceRef", "uid"),
		Entry("testID", "testID"),
		Entry("budget", "budget"),
		Entry("ttl", "ttl"),
	)

	DescribeTable("refuses a TestKey with an incorrect value",
		func(value any, path ...string) {
			u := rawTestKey("tk-bad")
			Expect(unstructured.SetNestedField(u.Object, value, append([]string{specField}, path...)...)).To(Succeed())
			expectInvalid(create(u))
		},
		Entry("empty Workspace name", "", "workspaceRef", "name"),
		Entry("Workspace name with capitals", "Dev", "workspaceRef", "name"),
		Entry("empty Workspace UID", "", "workspaceRef", "uid"),
		Entry("empty test ID", "", "testID"),
		Entry("test ID with a slash", "a/b", "testID"),
		Entry("test ID with a dot", "a.b", "testID"),
		Entry("test ID that starts with a hyphen", "-a", "testID"),
		Entry("test ID that starts with an underscore", "_a", "testID"),
		Entry("test ID with a space", "a b", "testID"),
		Entry("test ID with a new line", "a\n", "testID"),
		Entry("test ID with a tab", "a\tb", "testID"),
		Entry("test ID with a NUL", "a\x00b", "testID"),
		Entry("zero budget", "0", "budget"),
		Entry("zero budget with decimals", "0.000", "budget"),
		Entry("negative budget", "-1", "budget"),
		Entry("integer zero budget", int64(0), "budget"),
		Entry("budget that is not a Quantity", "five", "budget"),
		Entry("zero TTL", "0s", "ttl"),
		Entry("negative TTL", "-5m", "ttl"),
		Entry("TTL that is not a duration", "soon", "ttl"),
	)

	DescribeTable("refuses a change to the spec",
		func(change func(*corev1alpha1.TestKeySpec)) {
			tk := newTestKey("tk-immutable")
			Expect(create(tk)).To(Succeed())
			change(&tk.Spec)
			err := k8sClient.Update(ctx, tk)
			expectInvalid(err)
			Expect(err.Error()).To(ContainSubstring("spec is immutable"))
		},
		Entry("workspaceRef.name", func(s *corev1alpha1.TestKeySpec) { s.WorkspaceRef.Name = otherName }),
		Entry("workspaceRef.uid", func(s *corev1alpha1.TestKeySpec) { s.WorkspaceRef.UID = "ws-uid-2" }),
		Entry("testID", func(s *corev1alpha1.TestKeySpec) { s.TestID = "other-test" }),
		Entry("budget", func(s *corev1alpha1.TestKeySpec) { s.Budget = resource.MustParse("1") }),
		Entry("ttl", func(s *corev1alpha1.TestKeySpec) { s.TTL = metav1.Duration{Duration: time.Hour} }),
		Entry("budget by 1m", func(s *corev1alpha1.TestKeySpec) { s.Budget = resource.MustParse("501m") }),
		Entry("ttl by 1s", func(s *corev1alpha1.TestKeySpec) { s.TTL.Duration += time.Second }),
	)

	// The API server checks the result of the full patch. A remove and an add in one patch, or a
	// remove alone, before a controller sees the object, does not change the spec.
	DescribeTable("refuses a remove and an add of a spec field",
		func(patch string) {
			tk := newTestKey("tk-readd")
			Expect(create(tk)).To(Succeed())
			Expect(tk.Status).To(Equal(corev1alpha1.TestKeyStatus{}))
			expectInvalid(jsonPatch(tk, patch))

			got := &corev1alpha1.TestKey{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tk), got)).To(Succeed())
			want := newTestKey("tk-readd").Spec
			Expect(got.Spec.Budget.Cmp(want.Budget)).To(Equal(0))
			got.Spec.Budget, want.Budget = resource.Quantity{}, resource.Quantity{}
			Expect(got.Spec).To(Equal(want))
		},
		Entry("remove the spec", `[{"op":"remove","path":"/spec"}]`),
		Entry("remove testID", `[{"op":"remove","path":"/spec/testID"}]`),
		Entry("remove and add testID", `[{"op":"remove","path":"/spec/testID"},{"op":"add","path":"/spec/testID","value":"other"}]`),
		Entry("remove and add workspaceRef", `[{"op":"remove","path":"/spec/workspaceRef"},{"op":"add","path":"/spec/workspaceRef","value":{"name":"dev","uid":"ws-uid-2"}}]`),
		Entry("replace the spec", `[{"op":"replace","path":"/spec","value":{"workspaceRef":{"name":"dev","uid":"ws-uid-1"},"testID":"login-smoke","budget":"9","ttl":"30m"}}]`),
	)

	// A typed client writes a Quantity and a Duration in the canonical form, for example "500m" and
	// "30m0s". The same value in a different form is not a change to the spec.
	DescribeTable("accepts a change to the metadata and to the status",
		func(budget any, ttl, canonicalBudget, canonicalTTL string) {
			u := rawTestKey("tk-observe")
			Expect(unstructured.SetNestedField(u.Object, budget, specField, budgetField)).To(Succeed())
			Expect(unstructured.SetNestedField(u.Object, ttl, specField, ttlField)).To(Succeed())
			Expect(create(u)).To(Succeed())

			tk := &corev1alpha1.TestKey{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), tk)).To(Succeed())
			tk.Labels = map[string]string{"nookku.dev/run": "r1"}
			Expect(k8sClient.Update(ctx, tk)).To(Succeed())

			// The update stores the canonical form. The value is the same as the value at the create.
			stored := &unstructured.Unstructured{}
			stored.SetGroupVersionKind(u.GroupVersionKind())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), stored)).To(Succeed())
			Expect(stored.Object[specField]).To(HaveKeyWithValue(budgetField, canonicalBudget))
			Expect(stored.Object[specField]).To(HaveKeyWithValue(ttlField, canonicalTTL))
			storedBudget := resource.MustParse(canonicalBudget)
			Expect(storedBudget.Cmp(resource.MustParse(fmt.Sprint(budget)))).To(Equal(0))
			createdTTL, err := time.ParseDuration(ttl)
			Expect(err).NotTo(HaveOccurred())
			Expect(time.ParseDuration(canonicalTTL)).To(Equal(createdTTL))

			now := metav1.NewTime(time.Now().Truncate(time.Second))
			tk.Status = corev1alpha1.TestKeyStatus{
				KeyAlias:  "login-smoke-" + string(tk.UID),
				KeyHash:   strings.Repeat("0", 64),
				ExpiresAt: &now,
				RevokedAt: &now,
				Usage:     &corev1alpha1.AgentUsage{Spend: "0.01", Calls: 1},
				Conditions: []metav1.Condition{{Type: condRevoked, Status: metav1.ConditionTrue, Reason: "Expired",
					LastTransitionTime: now}},
			}
			Expect(k8sClient.Status().Update(ctx, tk)).To(Succeed())

			tk.Status.KeyHash = "not-a-hash"
			expectInvalid(k8sClient.Status().Update(ctx, tk))
		},
		Entry("the sample form", "0.50", "30m", "500m", "30m0s"),
		Entry("an integer budget", int64(1), "30m0s", "1", "30m0s"),
	)

	Context("Workspace test key limits", func() {
		newWorkspace := func(name string) *corev1alpha1.Workspace {
			return &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		}

		It("keeps a Workspace without the limits as before", func() {
			ws := newWorkspace("ws-no-testkeys")
			Expect(create(ws)).To(Succeed())
			got := &corev1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ws), got)).To(Succeed())
			Expect(got.Spec.TestKeys).To(BeNil())
		})

		It("accepts a Workspace with the limits", func() {
			ws := newWorkspace("ws-testkeys")
			ws.Spec.TestKeys = &corev1alpha1.WorkspaceTestKeys{
				MaxBudget: resource.MustParse("2"), MaxTTL: metav1.Duration{Duration: time.Hour}}
			Expect(create(ws)).To(Succeed())
		})

		DescribeTable("refuses incorrect limits",
			func(testKeys map[string]any) {
				u := &unstructured.Unstructured{Object: map[string]any{
					apiVersionField: corev1alpha1.GroupVersion.String(),
					kindField:       workspaceKind,
					metadataField:   map[string]any{nameField: "ws-bad-limits", namespaceField: ns},
					specField:       map[string]any{"testKeys": testKeys},
				}}
				expectInvalid(create(u))
			},
			Entry("empty limits", map[string]any{}),
			Entry("no maxBudget", map[string]any{maxTTLField: "1h"}),
			Entry("no maxTTL", map[string]any{maxBudgetField: "2"}),
			Entry("zero maxBudget", map[string]any{maxBudgetField: "0", maxTTLField: "1h"}),
			Entry("negative maxBudget", map[string]any{maxBudgetField: "-2", maxTTLField: "1h"}),
			Entry("zero maxTTL", map[string]any{maxBudgetField: "2", maxTTLField: "0s"}),
			Entry("negative maxTTL", map[string]any{maxBudgetField: "2", maxTTLField: "-1h"}),
		)
	})
})
