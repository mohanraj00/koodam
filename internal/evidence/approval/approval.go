package approval

import "time"

// Record is the evidence from a verified approval.
type Record struct {
	Source    string `json:"source"`
	Integrity string `json:"integrity"`
	Subject   string `json:"subject"`
	Verdict   string `json:"verdict"`
	ID        string `json:"id"`
	Digest    string `json:"digest"`
}

// Result gives the approval evidence and the status of its signing key.
type Result struct {
	Record    Record
	KeyStatus string
}

// Expected gives the scope, the function and the subject that the gate needs. The subject is
// the snapshot reference of the decision.
type Expected struct {
	Scope, Function, Subject string
}

// dateLayout is the layout of the dates from and until of a decision.
const dateLayout = "2006-01-02"

// The codes of a refusal of a decision that has a valid signature.
const (
	codeNotDecision     = "not_a_decision"
	codePurposeMismatch = "purpose_mismatch"
	codeSubjectMismatch = "subject_mismatch"
	codeInvalidWindow   = "invalid_window"
	codeNotYetValid     = "not_yet_valid"
	codeExpired         = "expired"
	codeNotHuman        = "not_human_verdict"
)

// Verify checks a signed decision against a trusted key set from the caller. The decision must
// have the expected scope, function and subject, and the UTC date of now must be in its
// half-open window from "from" to "until". Only a decision with the kind "human" gives evidence:
// a model or policy verdict, or a verdict without kind, never counts as a human verdict (Entelos
// KINDS.md, rule 3).
func Verify(signed, keySet []byte, want Expected, now time.Time) (Result, error) {
	record, err := verifySigned(signed, keySet)
	if err != nil {
		return Result{}, err
	}
	if record.Kind != "decision" {
		return Result{}, Refusal{codeNotDecision, ""}
	}
	if want.Scope == "" || want.Function == "" || record.Scope != want.Scope || record.Function != want.Function {
		return Result{}, Refusal{codePurposeMismatch, ""}
	}
	subject := record.Body.text("snapshot_ref")
	if !refPattern.MatchString(subject) || subject != want.Subject {
		return Result{}, Refusal{codeSubjectMismatch, ""}
	}
	from, okFrom := parseDate(record.Body.text("from"))
	until, okUntil := parseDate(record.Body.text("until"))
	if !okFrom || !okUntil || !from.Before(until) {
		return Result{}, Refusal{codeInvalidWindow, ""}
	}
	today, _ := parseDate(now.UTC().Format(dateLayout))
	if today.Before(from) {
		return Result{}, Refusal{codeNotYetValid, ""}
	}
	if !today.Before(until) {
		return Result{}, Refusal{codeExpired, ""}
	}
	if record.Body.text("kind") != "human" {
		return Result{}, Refusal{codeNotHuman, ""}
	}
	var verdict string
	switch record.Body.text("disposition") {
	case "accept":
		verdict = "pass"
	case "reject":
		verdict = "fail"
	default:
		return Result{}, Refusal{"invalid_disposition", ""}
	}
	return Result{
		Record: Record{Source: "approval", Integrity: "signature", Subject: subject,
			Verdict: verdict, ID: record.Ref, Digest: record.Ref},
		KeyStatus: record.KeyStatus,
	}, nil
}

// parseDate reads a calendar date in the layout YYYY-MM-DD. It refuses a date that is not real,
// for example 2026-02-30, as the Entelos core does.
func parseDate(text string) (time.Time, bool) {
	date, err := time.Parse(dateLayout, text)
	if err != nil || date.Year() < 1 || date.Format(dateLayout) != text {
		return time.Time{}, false
	}
	return date, true
}
