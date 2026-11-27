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
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// approvalSigner signs decisions for the scope "gate" and the function "pause" with a test key
// that the seed gives.
type approvalSigner struct {
	private ed25519.PrivateKey
	id      string
	keySet  string
}

func newApprovalSigner(seed byte) approvalSigner {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	text, id := base64.StdEncoding.EncodeToString(public), fmt.Sprintf("ed25519:%x", sha256.Sum256(public))
	return approvalSigner{private, id, fmt.Sprintf(`{"format":"entelos.keyset/1","signer":"test","keys":[`+
		`{"algorithm":"ed25519","key_id":%q,"public_key":%q,"status":"active"}]}`, id, text)}
}

// sign gives a signed human decision for the snapshot reference, with the decision time and the
// date range from "from" to "until".
func (s approvalSigner) sign(subject, disposition string, decided, from, until time.Time) []byte {
	const layout = "2006-01-02"
	body := fmt.Appendf(nil, `{"kind":"human","snapshot_ref":%q,"disposition":%q,"scope":"gate","function":"pause",`+
		`"format":"entelos.record/1","decided_at":%q,"from":%q,"until":%q}`, subject, disposition,
		decided.UTC().Format(time.RFC3339), from.UTC().Format(layout), until.UTC().Format(layout))
	envelope := append([]byte("application/vnd.cel-lifecycle.decision+json\x00"), body...)
	signature := ed25519.Sign(s.private, append([]byte("entelos.signature/1\x00"), envelope...))
	return fmt.Appendf(nil, `{"format":"entelos.record/1","record_kind":"decision","scope":"gate","function":"pause",`+
		`"record_ref":"sha256:%x","record":%q,"algorithm":"ed25519","key_id":%q,"signature":%q}`, sha256.Sum256(envelope),
		base64.StdEncoding.EncodeToString(body), s.id, base64.StdEncoding.EncodeToString(signature))
}

// approvalChain is the 3 parts of an ApprovalRecord.
type approvalChain struct{ signed, snapshot, manifest []byte }

// chain gives a signed human decision with a snapshot record and a manifest that bind the subject.
func (s approvalSigner) chain(subject, disposition string, decided, from, until time.Time) approvalChain {
	manifest := fmt.Appendf(nil, `{"format":"entelos.content/1","items":[{"item_id":"request","digest":%q}]}`, subject)
	snapshot := fmt.Appendf(nil, `{"format":"entelos.record/1","scope":"gate","candidate":{"kind":"content",`+
		`"ref":"sha256:%x"}}`+"\n", sha256.Sum256(manifest))
	ref := sha256.Sum256(append([]byte("application/vnd.cel-lifecycle.snapshot+json\x00"), snapshot...))
	return approvalChain{s.sign(fmt.Sprintf("sha256:%x", ref), disposition, decided, from, until), snapshot, manifest}
}

var _ = Describe("Agent approval", Ordered, func() {
	const day = 24 * time.Hour
	ctx := context.Background()
	signer, stranger := newApprovalSigner(1), newApprovalSigner(2)
	now := time.Now()
	var bound, bare string
	var count int

	BeforeAll(func() {
		for _, name := range []*string{&bound, &bare} {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "approval-"}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			*name = ns.Name
		}
		// The Profile of the namespace "bare" has no spec.approval.
		for name, trust := range map[string]*corev1alpha1.ProfileApproval{
			bound: {KeySet: signer.keySet, Scope: "gate", Function: "pause", MaxAgeSeconds: 3600}, bare: nil} {
			Expect(k8sClient.Create(ctx, &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{name}, Approval: trust}})).To(Succeed())
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, ns)).To(Succeed())
			ns.Labels = map[string]string{corev1alpha1.ProfileLabel: name}
			Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		}
	})

	carry := func(a *corev1alpha1.Agent, c approvalChain) {
		Expect(k8sClient.Create(ctx, &corev1alpha1.ApprovalRecord{
			ObjectMeta: metav1.ObjectMeta{GenerateName: a.Name + "-", Namespace: a.Namespace},
			Spec: corev1alpha1.ApprovalRecordSpec{Target: a.Name, SignedRecord: c.signed,
				SnapshotRecord: c.snapshot, Manifest: c.manifest}})).To(Succeed())
	}
	subjectOf := func(a *corev1alpha1.Agent) string {
		doc, err := approvalDocument(a)
		Expect(err).NotTo(HaveOccurred())
		return approvalSubject(doc)
	}
	// start makes an Agent that waits 1 hour, with the records that the function gives for its
	// subject. Then it runs one reconcile at the time "at".
	start := func(namespace string, records func(subject string) []approvalChain, at time.Time) (
		*corev1alpha1.Agent, *fakeGateway, reconcile.Result) {
		count++
		a := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("wait-%d", count), Namespace: namespace},
			Spec: corev1alpha1.AgentSpec{Image: testAgentImage, Command: []string{jobIngRunCommand}, Model: "m",
				Budget: resource.MustParse("1"), Approval: &corev1alpha1.AgentApprovalSpec{WaitSeconds: 3600}}}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		if records != nil {
			for _, c := range records(subjectOf(a)) {
				carry(a, c)
			}
		}
		gw := &fakeGateway{}
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Gateway: gw,
			GatewayURL: testGatewayURL, Grace: time.Minute, Now: func() time.Time { return at }}
		result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		return a, gw, result
	}
	// decide gives a chain with a decision that is 1 minute old and a date range around the time
	// of the test. old gives a chain with a decision that is 2 hours old.
	decide := func(s approvalSigner, subject, disposition string) approvalChain {
		return s.chain(subject, disposition, now.Add(-time.Minute), now.Add(-2*day), now.Add(2*day))
	}
	old := func(subject, disposition string) approvalChain {
		return signer.chain(subject, disposition, now.Add(-2*time.Hour), now.Add(-2*day), now.Add(2*day))
	}
	accept := func(subject string) approvalChain { return decide(signer, subject, "accept") }
	one := func(make func(subject string) approvalChain) func(string) []approvalChain {
		return func(subject string) []approvalChain { return []approvalChain{make(subject)} }
	}
	junk := approvalChain{[]byte("not a record"), []byte("{}"), []byte("{}")}
	other := fmt.Sprintf("sha256:%064x", 7)

	DescribeTable("gives each state of the condition Approval", func(inBare bool, records func(string) []approvalChain,
		late time.Duration, status metav1.ConditionStatus, reason, text string) {
		namespace := bound
		if inBare {
			namespace = bare
		}
		a, gw, result := start(namespace, records, now.Add(late))
		cond := meta.FindStatusCondition(a.Status.Conditions, corev1alpha1.AgentApproval)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(status), cond.Message)
		Expect(cond.Reason).To(Equal(reason), cond.Message)
		Expect(cond.Message).To(ContainSubstring(text))
		// The status has the request and its subject for an approver integration.
		Expect(a.Status.Approval).To(HaveField("Subject", subjectOf(a)))
		Expect(approvalSubject([]byte(a.Status.Approval.Request))).To(Equal(subjectOf(a)))
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: runName(a)}, &batchv1.Job{})
		switch status {
		case metav1.ConditionTrue:
			Expect(err).NotTo(HaveOccurred())
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			Expect(gw.created).To(HaveLen(1))
			Expect(a.Status.Evidence).To(HaveField("Subject", subjectOf(a)))
			Expect(a.Status.Evidence).To(HaveField("Verdict", corev1alpha1.EvidencePass))
			Expect(a.Status.Evidence).To(HaveField("Integrity", corev1alpha1.EvidenceSignature))
		case metav1.ConditionFalse:
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
			Expect(meta.FindStatusCondition(a.Status.Conditions, condRevoked).Reason).To(Equal(reason))
		default:
			// A paused run has no finalizer, and it gets a new check in 1 minute or less.
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentPending))
			Expect(controllerutil.ContainsFinalizer(a, finalizer)).To(BeFalse())
			Expect(result.RequeueAfter).To(And(BeNumerically(">", 0), BeNumerically("<=", approvalRecheck)))
		}
		if status != metav1.ConditionTrue {
			// A run that does not start has no Job and no key.
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			Expect(gw.created).To(BeEmpty())
			Expect(a.Status.KeyHash).To(BeEmpty())
			Expect(a.Status.Evidence != nil).To(Equal(reason == corev1alpha1.ApprovalDenied))
		}
	},
		Entry("Waiting: no record", false, nil, time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalWaiting, "sha256:"),
		Entry("Approved: a signed accept, also with records that the verifier refuses", false,
			func(s string) []approvalChain { return []approvalChain{junk, decide(stranger, s, "reject"), accept(s)} },
			time.Duration(0), metav1.ConditionTrue, corev1alpha1.ApprovalApproved, "valid accept"),
		Entry("Denied: a signed reject, also with a signed accept", false,
			func(s string) []approvalChain { return []approvalChain{accept(s), decide(signer, s, "reject")} },
			time.Duration(0), metav1.ConditionFalse, corev1alpha1.ApprovalDenied, "valid reject"),
		Entry("Expired: a decision that is older than the maximum age", false,
			one(func(s string) approvalChain { return old(s, "accept") }),
			time.Duration(0), metav1.ConditionFalse, corev1alpha1.ApprovalExpired, "maximum age of 3600 seconds"),
		Entry("Expired: an until date in the past", false,
			one(func(s string) approvalChain {
				return signer.chain(s, "accept", now.Add(-time.Minute), now.Add(-3*day), now.Add(-day))
			}), time.Duration(0), metav1.ConditionFalse, corev1alpha1.ApprovalExpired, "until date"),
		Entry("NotYetValid: a from date in the future", false,
			one(func(s string) approvalChain {
				return signer.chain(s, "accept", now.Add(-time.Minute), now.Add(2*day), now.Add(4*day))
			}), time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalNotYetValid, "from date"),
		Entry("SubjectMismatch: a signed accept of a different run", false,
			one(func(string) approvalChain { return accept(other) }),
			time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalSubjectMismatch, "does not bind the subject"),
		Entry("SubjectMismatch: an old signed reject of a different run does not stop the run", false,
			one(func(string) approvalChain { return old(other, "reject") }),
			time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalSubjectMismatch, "does not bind the subject"),
		Entry("SubjectMismatch: a snapshot record with one more byte", false,
			one(func(s string) approvalChain {
				c := accept(s)
				c.snapshot = append(c.snapshot, ' ')
				return c
			}), time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalSubjectMismatch, "does not bind the subject"),
		Entry("NotVerified: a manifest with one more byte", false,
			one(func(s string) approvalChain {
				c := accept(s)
				c.manifest = append(c.manifest, ' ')
				return c
			}), time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalNotVerified, "manifest_ref: binding_mismatch"),
		Entry("NotVerified: a reject from a key that the key set does not have", false,
			one(func(s string) approvalChain { return decide(stranger, s, "reject") }),
			time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalNotVerified, "unknown_key"),
		Entry("NotVerified: a decision time in the future", false,
			one(func(s string) approvalChain {
				return signer.chain(s, "reject", now.Add(time.Hour), now.Add(-2*day), now.Add(2*day))
			}), time.Duration(0), metav1.ConditionUnknown, corev1alpha1.ApprovalNotVerified, "invalid_decided_at"),
		Entry("NoKeySet: a Profile with no spec.approval", true, one(accept),
			time.Duration(0), metav1.ConditionFalse, corev1alpha1.ApprovalNoKeySet, "spec.approval"),
		Entry("ApprovalTimeout: no accept before the wait ends", false,
			func(string) []approvalChain { return []approvalChain{junk} },
			2*time.Hour, metav1.ConditionFalse, corev1alpha1.ApprovalTimeout, "3600 seconds"),
	)

	It("pauses the run again when the spec changes before the Job starts", func() {
		a, _, _ := start(bound, nil, now)
		before := subjectOf(a)
		a.Spec.Command = []string{"changed"}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		Expect(subjectOf(a)).NotTo(Equal(before))
		carry(a, accept(before))
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Gateway: &fakeGateway{}}
		// The record maps to the Agents of its namespace that wait.
		Expect(r.approvalWaiters(ctx, &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: bound}})).
			To(ContainElement(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)}))
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		Expect(meta.FindStatusCondition(a.Status.Conditions, corev1alpha1.AgentApproval).Reason).
			To(Equal(corev1alpha1.ApprovalSubjectMismatch))
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentPending))
	})

	It("stops a run that has a key when the approval is not there at the next start attempt", func() {
		a, _, _ := start(bound, nil, now)
		carry(a, accept(subjectOf(a)))
		gw := &fakeGateway{createErr: errors.New("the gateway is down")}
		r := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Gateway: gw}
		req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)}
		_, err := r.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())
		Expect(k8sClient.Get(ctx, req.NamespacedName, a)).To(Succeed())
		hash := a.Status.KeyHash
		Expect(hash).NotTo(BeEmpty())
		a.Spec.Command = []string{"edited"}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		gw.createErr = nil
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, req.NamespacedName, a)).To(Succeed())
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		Expect(gw.blocked).To(Equal([]string{hash}))
		Expect(gw.created).To(BeEmpty())
		Expect(meta.FindStatusCondition(a.Status.Conditions, corev1alpha1.AgentApproval).Reason).
			To(Equal(corev1alpha1.ApprovalSubjectMismatch))
	})

	It("reads a bounded count of records in one check", func() {
		a, _, _ := start(bound, func(string) []approvalChain {
			return slices.Repeat([]approvalChain{junk}, approvalMaxRecords+approvalPageSize)
		}, now)
		Expect(meta.FindStatusCondition(a.Status.Conditions, corev1alpha1.AgentApproval).Message).
			To(ContainSubstring(fmt.Sprintf("read only %d records", approvalMaxRecords)))
	})

	It("gives the states of the fixture chains of the pinned release", func() {
		read := func(name string) []byte {
			raw, err := os.ReadFile(filepath.Join("..", "evidence", "approval", "testdata", "v0.7.0", name))
			Expect(err).NotTo(HaveOccurred())
			return raw
		}
		trust := &corev1alpha1.ProfileApproval{KeySet: string(read("keyset.json")), Scope: "fixture",
			Function: "content:agent-approvals/egress-grant", MaxAgeSeconds: 3600}
		subject := approvalSubject(read("request.json"))
		decided := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
		for name, reason := range map[string]string{"accept": corev1alpha1.ApprovalApproved, "reject": corev1alpha1.ApprovalDenied} {
			rec := &corev1alpha1.ApprovalRecord{Spec: corev1alpha1.ApprovalRecordSpec{SignedRecord: read("decision-" + name + ".signed.json"),
				SnapshotRecord: read("snapshot-" + name + ".record"), Manifest: read("manifest-" + name + ".json")}}
			s := checkRecord(rec, trust, subject, decided.Add(time.Minute))
			Expect(s.reason).To(Equal(reason), s.message)
			Expect(s.evidence).To(HaveField("Subject", subject))
			Expect(checkRecord(rec, trust, subject, decided.Add(2*time.Hour)).reason).To(Equal(corev1alpha1.ApprovalExpired))
			Expect(checkRecord(rec, trust, other, decided.Add(time.Minute)).reason).To(Equal(corev1alpha1.ApprovalSubjectMismatch))
		}
	})

	It("makes the request document with a fixed form", func() {
		a := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-1", Name: "job-7", UID: "uid-1"},
			Spec: corev1alpha1.AgentSpec{Image: testAgentImage, Model: "m"}}
		spec := `{"image":"img","command":null,"model":"m","budget":"0"}`
		Expect(approvalDocument(a)).To(BeEquivalentTo(fmt.Sprintf(`{"format":"koodam.dev/approval-request/v1",`+
			`"namespace":"ns-1","agent":"job-7","uid":"uid-1","specSHA256":"%x","grant":{}}`, sha256.Sum256([]byte(spec)))))
		first := subjectOf(a)
		a.UID = "uid-2"
		Expect(subjectOf(a)).NotTo(Equal(first))
	})
})
