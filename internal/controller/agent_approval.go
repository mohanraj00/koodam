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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/evidence/approval"
)

const (
	// approvalRequestFormat is the format of the request document of an approval.
	approvalRequestFormat = "koodam.dev/approval-request/v1"
	// approvalRecheck is the most time that a paused run waits before it reads the records and
	// the Profile again. A new ApprovalRecord also starts a new check.
	approvalRecheck = time.Minute
	// approvalPageSize is the count of ApprovalRecords in one list call. A record has 1,000,000
	// bytes or fewer, so a small page keeps the memory of a check small.
	approvalPageSize = 4
	// approvalMaxRecords is the maximum of ApprovalRecords that one check reads for an Agent. It
	// bounds the work of a check if a writer makes many records.
	approvalMaxRecords = 64
	// approvalTargetField is the selectable field of the ApprovalRecord CRD.
	approvalTargetField = "spec.target"
)

// approvalRequest is the request document of an approval. Its JSON text has these keys in this
// order and no white space. The grant is empty: the run asks for no extra permission.
type approvalRequest struct {
	Format     string   `json:"format"`
	Namespace  string   `json:"namespace"`
	Agent      string   `json:"agent"`
	UID        string   `json:"uid"`
	SpecSHA256 string   `json:"specSHA256"`
	Grant      struct{} `json:"grant"`
}

// approvalDocument gives the request document of the run of a. The document has the SHA-256 of
// the JSON text of the spec, so a change of the spec gives a different document.
func approvalDocument(a *corev1alpha1.Agent) ([]byte, error) {
	spec, err := json.Marshal(a.Spec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(approvalRequest{Format: approvalRequestFormat, Namespace: a.Namespace, Agent: a.Name,
		UID: string(a.UID), SpecSHA256: fmt.Sprintf("%x", sha256.Sum256(spec))})
}

// approvalSubject gives the subject of a request document: sha256: and the SHA-256 of its bytes.
func approvalSubject(doc []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(doc)) }

// decisionSubject gives the snapshot reference that the decision in rec must name for the
// subject of the run. The snapshot record of rec must have the scope and a content candidate, and
// the manifest of rec must have the subject as its one item.
func decisionSubject(rec *corev1alpha1.ApprovalRecord, scope, subject string) (string, error) {
	return approval.Binding(rec.Spec.SnapshotRecord, rec.Spec.Manifest, scope, subject)
}

// approvalState is one state of the condition Approval.
type approvalState struct {
	status          metav1.ConditionStatus
	reason, message string
	// evidence is the verified record of an accept or of a reject.
	evidence *approval.Record
}

// approvalRank gives the order of the states if the records of an Agent give more than one. A
// reject comes before an accept. A record that the verifier refused has the lowest rank of the
// records, so a writer with no signing key cannot change the result of a signed record.
var approvalRank = map[string]int{
	corev1alpha1.ApprovalWaiting: 0, corev1alpha1.ApprovalNotVerified: 1, corev1alpha1.ApprovalSubjectMismatch: 2,
	corev1alpha1.ApprovalNotYetValid: 3, corev1alpha1.ApprovalExpired: 4, corev1alpha1.ApprovalApproved: 5,
	corev1alpha1.ApprovalDenied: 6,
}

// checkRecord verifies the signed decision of rec with the trust of the Profile and gives its
// state. It is the only call of the verifier. The message has the name of the record and the
// code of the verifier. It never has bytes of the record.
func checkRecord(rec *corev1alpha1.ApprovalRecord, trust *corev1alpha1.ProfileApproval, subject string,
	now time.Time) approvalState {
	state := func(status metav1.ConditionStatus, reason, format string, args ...any) approvalState {
		return approvalState{status: status, reason: reason, message: fmt.Sprintf("The record %s "+format, append([]any{rec.Name}, args...)...)}
	}
	// The binding comes first. Thus a signed decision of a different run, for example an old one,
	// gives SubjectMismatch or NotVerified, and never a state that stops the run.
	var result approval.Result
	ref, err := decisionSubject(rec, trust.Scope, subject)
	if err == nil {
		result, err = approval.Verify(rec.Spec.SignedRecord, []byte(trust.KeySet), approval.Expected{
			Scope: trust.Scope, Function: trust.Function, Subject: ref}, now)
	}
	code, text := "unknown", "unknown"
	if refusal, ok := errors.AsType[approval.Refusal](err); ok {
		code, text = refusal.Code, refusal.Error()
	}
	// The from and until dates are the date range of the approved revision. The expiry of the
	// approval is the age of the decision, and a signature has no trusted time.
	decided, ok := approval.DecidedAt(rec.Spec.SignedRecord)
	switch {
	case code == approval.CodeExpired:
		return state(metav1.ConditionFalse, corev1alpha1.ApprovalExpired, "has an until date that is not after the date of the check.")
	case code == approval.CodeNotYetValid:
		return state(metav1.ConditionUnknown, corev1alpha1.ApprovalNotYetValid, "has a from date in the future.")
	case code == approval.CodeSubjectMismatch:
		return state(metav1.ConditionUnknown, corev1alpha1.ApprovalSubjectMismatch,
			"does not bind the subject of the run, which is %s.", subject)
	case err != nil:
		return state(metav1.ConditionUnknown, corev1alpha1.ApprovalNotVerified, "did not pass the verifier: %s.", text)
	case !ok || decided.After(now):
		return state(metav1.ConditionUnknown, corev1alpha1.ApprovalNotVerified, "did not pass the verifier: invalid_decided_at.")
	case now.Sub(decided) > time.Duration(trust.MaxAgeSeconds)*time.Second:
		return state(metav1.ConditionFalse, corev1alpha1.ApprovalExpired,
			"has a decision that is older than the maximum age of %d seconds.", trust.MaxAgeSeconds)
	}
	s := state(metav1.ConditionFalse, corev1alpha1.ApprovalDenied, "has a valid reject for the subject %s.", subject)
	if result.Record.Verdict == string(corev1alpha1.EvidencePass) {
		s = state(metav1.ConditionTrue, corev1alpha1.ApprovalApproved, "has a valid accept for the subject %s.", subject)
	}
	// The subject of the evidence is the subject of the run, and not the snapshot reference.
	result.Record.Subject = subject
	s.evidence = &result.Record
	return s
}

// approvalState reads the Profile of the namespace and the ApprovalRecords of a, and gives the
// state with the highest rank. It reads from the API server in small pages, so the manager keeps
// no record in its cache. It reads approvalMaxRecords records at most.
func (r *AgentReconciler) approvalState(ctx context.Context, a *corev1alpha1.Agent, subject string,
	now time.Time) (approvalState, error) {
	p, err := boundProfileObject(ctx, r.APIReader, a.Namespace)
	if err != nil {
		return approvalState{}, err
	}
	if p == nil || p.Spec.Approval == nil {
		return approvalState{metav1.ConditionFalse, corev1alpha1.ApprovalNoKeySet,
			"The Profile of the namespace has no spec.approval.", nil}, nil
	}
	best := approvalState{metav1.ConditionUnknown, corev1alpha1.ApprovalWaiting,
		fmt.Sprintf("No ApprovalRecord targets the Agent. The subject of the run is %s.", subject), nil}
	list := &corev1alpha1.ApprovalRecordList{}
	for token, read := "", 0; ; {
		if err := r.APIReader.List(ctx, list, client.InNamespace(a.Namespace),
			client.MatchingFields{approvalTargetField: a.Name}, client.Limit(approvalPageSize),
			client.Continue(token)); err != nil {
			return approvalState{}, err
		}
		for i := range list.Items {
			if s := checkRecord(&list.Items[i], p.Spec.Approval, subject, now); approvalRank[s.reason] > approvalRank[best.reason] {
				best = s
			}
		}
		token, read = list.Continue, read+len(list.Items)
		if token == "" {
			break
		}
		if read >= approvalMaxRecords {
			// A record that the check did not read can have a higher rank. Only Denied has the
			// highest rank, so each other state waits: an incomplete check never starts a run.
			if best.reason != corev1alpha1.ApprovalDenied {
				best = approvalState{metav1.ConditionUnknown, corev1alpha1.ApprovalWaiting,
					fmt.Sprintf("The check read only %d records of the Agent, so it cannot start the run. "+
						"Delete the ApprovalRecords that are not necessary. The subject of the run is %s.", read, subject), nil}
			}
			break
		}
	}
	return best, nil
}

// approvalWait is the result of provision when the run waits for an approval. Reconcile requeues
// the Agent after the time, and the run does not fail.
type approvalWait struct{ after time.Duration }

func (w approvalWait) Error() string { return "the run waits for an approval" }

// awaitApproval holds the run of an Agent with spec.approval until the condition Approval is
// True. admit calls it before the finalizer, the budget reservation and the key, so a paused run
// has no key and no pod. Each provision attempt does the check again, until the Job starts: a change of
// the spec, a later reject or a decision that is too old then keeps or stops a run that did not
// start. It is true if the run stopped. A paused run gives an approvalWait error.
func (r *AgentReconciler) awaitApproval(ctx context.Context, a *corev1alpha1.Agent) (bool, error) {
	if a.Spec.Approval == nil {
		return false, nil
	}
	doc, err := approvalDocument(a)
	if err != nil {
		return false, err
	}
	now, subject := r.now(), approvalSubject(doc)
	s, err := r.approvalState(ctx, a, subject, now)
	if err != nil {
		return false, err
	}
	deadline := a.CreationTimestamp.Add(time.Duration(a.Spec.Approval.WaitSeconds) * time.Second)
	if s.status == metav1.ConditionUnknown && !now.Before(deadline) {
		s = approvalState{metav1.ConditionFalse, corev1alpha1.ApprovalTimeout, fmt.Sprintf(
			"The run got no approval in %d seconds.", a.Spec.Approval.WaitSeconds), nil}
	}
	before := a.Status.DeepCopy()
	// An approver integration reads the request and the subject from the status.
	a.Status.Approval = &corev1alpha1.AgentApprovalStatus{Request: string(doc), Subject: subject}
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{Type: corev1alpha1.AgentApproval,
		Status: s.status, Reason: s.reason, Message: s.message, ObservedGeneration: a.Generation})
	setApprovalEvidence(a, s.evidence, now)
	// A run with a key hash had an approval at an earlier start attempt. If the approval is not
	// there now, for example after a change of the spec, the run stops: a paused run must have no
	// key. The revoke blocks the key first.
	if s.status == metav1.ConditionFalse || (s.status == metav1.ConditionUnknown && a.Status.KeyHash != "") {
		logf.FromContext(ctx).Info("The approval stops the run", "reason", s.reason)
		return true, r.fail(ctx, a, corev1alpha1.AgentFailed, s.reason)
	}
	if s.status == metav1.ConditionUnknown {
		a.Status.Phase = corev1alpha1.AgentPending
	}
	// Write the status only if it changed, because each write starts a new reconcile.
	if !equality.Semantic.DeepEqual(before, &a.Status) {
		if err := r.Status().Update(ctx, a); err != nil {
			return false, err
		}
	}
	if s.status == metav1.ConditionUnknown {
		return false, approvalWait{after: min(deadline.Sub(now), approvalRecheck)}
	}
	return false, nil
}

// setApprovalEvidence writes the record of an accept or of a reject to the status. It keeps a
// record that is there for the same decision, so the time of the first check stays. A state with
// no accepted decision has no record.
func setApprovalEvidence(a *corev1alpha1.Agent, rec *approval.Record, now time.Time) {
	if rec == nil {
		a.Status.Evidence = nil
		return
	}
	if old := a.Status.Evidence; old != nil && old.ID == rec.ID && old.Subject == rec.Subject &&
		string(old.Verdict) == rec.Verdict {
		return
	}
	a.Status.Evidence = &corev1alpha1.EvidenceRecord{Source: corev1alpha1.EvidenceSourceApproval,
		Subject: rec.Subject, Verdict: corev1alpha1.EvidenceVerdict(rec.Verdict), ID: rec.ID, Digest: rec.Digest,
		Integrity: corev1alpha1.EvidenceSignature, CheckedAt: metav1.NewTime(now)}
}

// approvalWaiters maps an ApprovalRecord to the Agents of its namespace that can wait for an
// approval. The watch has only the metadata of the record, so the target is not known here.
func (r *AgentReconciler) approvalWaiters(ctx context.Context, o client.Object) []reconcile.Request {
	list := &corev1alpha1.AgentList{}
	if err := r.List(ctx, list, client.InNamespace(o.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Cannot list the Agents that wait for an approval")
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		if a := &list.Items[i]; a.Spec.Approval != nil && (a.Status.Phase == "" || a.Status.Phase == corev1alpha1.AgentPending) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		}
	}
	return out
}
