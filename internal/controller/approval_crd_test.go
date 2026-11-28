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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// These specs apply the CRDs to the envtest API server. They run no controller.
var _ = Describe("Approval CRD rules", func() {
	ctx := context.Background()
	const prefix = "approval-crd-"
	create := func(o client.Object) error {
		o.SetNamespace(metav1.NamespaceDefault)
		err := k8sClient.Create(ctx, o)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed()) })
		}
		return err
	}
	invalid := func(err error, text string) {
		GinkgoHelper()
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}
	record := func(target string, signed []byte) *corev1beta1.ApprovalRecord {
		return &corev1beta1.ApprovalRecord{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix},
			Spec: corev1beta1.ApprovalRecordSpec{Target: target, SignedRecord: signed,
				SnapshotRecord: []byte("{}"), Manifest: []byte("{}")}}
	}
	agent := func(approval *corev1beta1.AgentApprovalSpec) *corev1beta1.Agent {
		return &corev1beta1.Agent{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix},
			Spec: corev1beta1.AgentSpec{Image: testAgentImage, Command: []string{jobIngRunCommand}, Model: "m",
				Budget: resource.MustParse("1"), Approval: approval}}
	}

	It("limits the bytes of an ApprovalRecord", func() {
		// The largest record that the rules permit goes into the store.
		largest := record("job-7", bytes.Repeat([]byte("a"), 1000000))
		largest.Spec.SnapshotRecord, largest.Spec.Manifest = bytes.Repeat([]byte("b"), 65536), bytes.Repeat([]byte("c"), 65536)
		Expect(create(largest)).To(Succeed())
		for _, change := range []func(*corev1beta1.ApprovalRecordSpec) string{
			func(s *corev1beta1.ApprovalRecordSpec) string { s.SnapshotRecord = nil; return "spec.snapshotRecord" },
			func(s *corev1beta1.ApprovalRecordSpec) string { s.Manifest = nil; return "spec.manifest" },
			func(s *corev1beta1.ApprovalRecordSpec) string {
				s.SnapshotRecord = bytes.Repeat([]byte("b"), 65537)
				return "65,536 bytes"
			},
			func(s *corev1beta1.ApprovalRecordSpec) string {
				s.Manifest = bytes.Repeat([]byte("c"), 65537)
				return "65,536 bytes"
			},
		} {
			r := record("job-7", []byte("{}"))
			text := change(&r.Spec)
			invalid(create(r), text)
		}
		invalid(create(record("job-7", bytes.Repeat([]byte("a"), 1000001))), "1,000,000 bytes")
		invalid(create(record("job-7", bytes.Repeat([]byte("a"), 1000003))), "spec.signedRecord")
		invalid(create(record("job-7", nil)), "spec.signedRecord")
		invalid(create(record("Job_7", []byte("{}"))), "spec.target")
	})

	It("selects the ApprovalRecords of one Agent by spec.target", func() {
		Expect(create(record("select-a", []byte("{}")))).To(Succeed())
		Expect(create(record("select-b", []byte("{}")))).To(Succeed())
		list := &corev1beta1.ApprovalRecordList{}
		Expect(k8sClient.List(ctx, list, client.InNamespace(metav1.NamespaceDefault),
			client.MatchingFields{"spec.target": "select-b"})).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
		Expect(list.Items[0].Spec.Target).To(Equal("select-b"))
	})

	It("keeps the spec of an ApprovalRecord immutable", func() {
		r := record("job-7", []byte("{}"))
		Expect(create(r)).To(Succeed())
		r.Spec.SignedRecord = []byte("[]")
		invalid(k8sClient.Update(ctx, r), "spec is immutable")
		r.Spec.SignedRecord, r.Spec.Target = []byte("{}"), "job-8"
		invalid(k8sClient.Update(ctx, r), "spec is immutable")
	})

	It("gives spec.approval of an Agent a default wait and keeps it immutable", func() {
		a := agent(&corev1beta1.AgentApprovalSpec{})
		Expect(create(a)).To(Succeed())
		Expect(a.Spec.Approval.WaitSeconds).To(Equal(int64(86400)))
		a.Spec.Approval.WaitSeconds = 60
		invalid(k8sClient.Update(ctx, a), "spec.approval is immutable")
		a.Spec.Approval = nil
		invalid(k8sClient.Update(ctx, a), "spec.approval is immutable")

		// The status takes the request and a subject of the fixed form.
		a.Status.Approval = &corev1beta1.AgentApprovalStatus{Request: "{}", Subject: "sha256:" + strings.Repeat("0", 64)}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		a.Status.Approval.Subject = "sha256:short"
		invalid(k8sClient.Status().Update(ctx, a), "status.approval.subject")

		none := agent(nil)
		Expect(create(none)).To(Succeed())
		none.Spec.Approval = &corev1beta1.AgentApprovalSpec{WaitSeconds: 60}
		invalid(k8sClient.Update(ctx, none), "spec.approval is immutable")
		invalid(create(agent(&corev1beta1.AgentApprovalSpec{WaitSeconds: 2592001})), "spec.approval.waitSeconds")
	})

	DescribeTable("checks spec.approval of a Profile", func(keySet, scope, function string, maxAge int64, text string) {
		p := &corev1beta1.Profile{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix},
			Spec: corev1beta1.ProfileSpec{Approval: &corev1beta1.ProfileApproval{
				KeySet: keySet, Scope: scope, Function: function, MaxAgeSeconds: maxAge}}}
		err := create(p)
		if text == "" {
			Expect(err).NotTo(HaveOccurred())
		} else {
			invalid(err, text)
		}
	},
		Entry("permits a key set, a scope, a function and a maximum age", "{}", "payments", "release", int64(3600), ""),
		Entry("refuses an empty key set", "", "payments", "release", int64(3600), "spec.approval.keySet"),
		Entry("refuses an empty scope", "{}", "", "release", int64(3600), "spec.approval.scope"),
		Entry("refuses an empty function", "{}", "payments", "", int64(3600), "spec.approval.function"),
		Entry("refuses a Profile with no maximum age", "{}", "payments", "release", int64(0), "spec.approval.maxAgeSeconds"),
	)
})
