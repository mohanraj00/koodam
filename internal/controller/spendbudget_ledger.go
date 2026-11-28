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
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// The period budget of a namespace. The SpendBudget status is a ledger: spend is the settled spend
// of the runs that started in the period and ended, and reserved is the sum of the budgets of the
// active runs. A run starts only if spend + reserved + its budget is not more than the limit. The
// status update with the resourceVersion is the lock. The gateway applies the budget of each run,
// so the spend of the period cannot go above the limit.

// DefaultBudgetRetry is the most time that a waiting run waits before it reads the budget again.
// A change of the SpendBudget also starts a new check.
const DefaultBudgetRetry = time.Minute

// maxReservations is the maximum of active runs in a ledger. It is the MaxItems of the CRD.
const maxReservations = 500

// budgetPeriod gives the UTC day or the UTC month that has t.
func budgetPeriod(p corev1beta1.SpendPeriod, t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	if p == corev1beta1.SpendPeriodMonth {
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 1)
}

func (r *AgentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *AgentReconciler) budgetRetry() time.Duration {
	if r.BudgetRetry > 0 {
		return r.BudgetRetry
	}
	return DefaultBudgetRetry
}

// quantity reads a decimal string of the ledger. The controller writes these strings, so an empty
// or a bad value is zero.
func quantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.Quantity{}
	}
	return q
}

// decimal writes q as a plain decimal string with no trailing zeros, for example "6.25".
func decimal(q resource.Quantity) string {
	s := q.AsDec().String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func agentEnded(a *corev1beta1.Agent) bool {
	return a.Status.Phase == corev1beta1.AgentSucceeded || a.Status.Phase == corev1beta1.AgentFailed
}

func olderFirst(x, y corev1beta1.Agent) int {
	if c := x.CreationTimestamp.Compare(y.CreationTimestamp.Time); c != 0 {
		return c
	}
	return strings.Compare(x.Name, y.Name)
}

func reservation(b *corev1beta1.SpendBudget, uid types.UID) int {
	return slices.IndexFunc(b.Status.Reservations, func(s corev1beta1.SpendReservation) bool {
		return s.RunID == string(uid)
	})
}

func (r *AgentReconciler) spendBudget(ctx context.Context, ns string) (*corev1beta1.SpendBudget, error) {
	b := &corev1beta1.SpendBudget{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: corev1beta1.SpendBudgetName}, b)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return b, err
}

// waitingRuns gives the runs of the namespace that wait for the budget, oldest first.
func (r *AgentReconciler) waitingRuns(ctx context.Context, ns string, b *corev1beta1.SpendBudget) (
	[]corev1beta1.Agent, error) {
	list := &corev1beta1.AgentList{}
	if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	var out []corev1beta1.Agent
	for _, o := range list.Items {
		c := meta.FindStatusCondition(o.Status.Conditions, condProvisioned)
		if c == nil || c.Reason != corev1beta1.ReasonPeriodBudgetExceeded || agentEnded(&o) ||
			admitted(&o) || !o.DeletionTimestamp.IsZero() || reservation(b, o.UID) >= 0 {
			continue
		}
		out = append(out, o)
	}
	slices.SortFunc(out, olderFirst)
	return out, nil
}

// roll moves the ledger to the period that has now. When a period ends, the ledger drops the
// reservations of that period: the spend of a run counts only in the period where it started. It
// rebuilds the ledger from the Agents if the status has no period (a new budget or a lost status),
// or if spec.period changed. A change of spec.period gives a status period that is not a period of
// the new type, also after the old period ended. A rebuild cannot see the spend of a deleted run,
// so if the old period overlaps the new period, the rebuild keeps the old spend when it is more.
func (r *AgentReconciler) roll(ctx context.Context, b *corev1beta1.SpendBudget, now time.Time) error {
	start, end := budgetPeriod(b.Spec.Period, now)
	s := &b.Status
	if s.PeriodStart != nil && s.PeriodEnd != nil && s.PeriodStart.Equal(&metav1.Time{Time: start}) &&
		s.PeriodEnd.Equal(&metav1.Time{Time: end}) {
		return nil
	}
	rebuild := s.PeriodStart == nil || s.PeriodEnd == nil || now.Before(s.PeriodEnd.Time)
	if !rebuild {
		oldStart, oldEnd := budgetPeriod(b.Spec.Period, s.PeriodStart.Time)
		rebuild = !oldStart.Equal(s.PeriodStart.Time) || !oldEnd.Equal(s.PeriodEnd.Time)
	}
	var kept resource.Quantity
	if s.PeriodStart != nil && s.PeriodEnd != nil && s.PeriodStart.Before(&metav1.Time{Time: end}) &&
		start.Before(s.PeriodEnd.Time) {
		kept = quantity(s.Spend)
	}
	s.PeriodStart, s.PeriodEnd = &metav1.Time{Time: start}, &metav1.Time{Time: end}
	s.Spend, s.Reserved, s.Reservations = "0", "0", nil
	meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: corev1beta1.SpendBudgetReady,
		Status: metav1.ConditionTrue, Reason: "PeriodStarted", ObservedGeneration: b.Generation,
		Message: "The ledger counts the Agent runs that start in the period."})
	if !rebuild {
		return nil
	}
	// The rebuild reads the Agents from the API server. The cache can miss the keyHash of a run that
	// the controller admitted a moment ago, and the rebuild would then not reserve its budget.
	list := &corev1beta1.AgentList{}
	if err := r.APIReader.List(ctx, list, client.InNamespace(b.Namespace)); err != nil {
		return err
	}
	rebuildLedger(s, list.Items, start, end, b.Spec.Spend, maxReservations)
	if kept.Cmp(quantity(s.Spend)) > 0 {
		s.Spend = decimal(kept)
	}
	return nil
}

// rebuildLedger counts the runs that have a key in the ledger of the period from start to end. The
// ledger has a reservation for at most maxRuns active runs. The budget of each other active run is
// in reserved too, with no reservation, so the limit check counts it until the period ends. A run
// with a key and no valid status.budget got its key before the ledger existed. Its spec.budget can have
// changed after the key, so the rebuild counts the limit for that run: no other run starts until
// it ends.
func rebuildLedger(s *corev1beta1.SpendBudgetStatus, runs []corev1beta1.Agent, start, end time.Time,
	limit resource.Quantity, maxRuns int) {
	slices.SortFunc(runs, olderFirst)
	var spend, reserved resource.Quantity
	for _, o := range runs {
		if o.Status.KeyHash == "" {
			continue
		}
		admitted, err := resource.ParseQuantity(o.Status.Budget)
		if err != nil {
			admitted = limit.DeepCopy()
		}
		// An ended run keeps its reservation until the revoke blocked its key and read its usage.
		if !agentEnded(&o) || o.Status.RevokedAt == nil {
			if len(s.Reservations) < maxRuns {
				s.Reservations = append(s.Reservations, corev1beta1.SpendReservation{
					RunID: string(o.UID), Agent: o.Name, Budget: decimal(admitted)})
			}
			reserved.Add(admitted)
			continue
		}
		began := o.CreationTimestamp.Time
		if o.Status.StartedAt != nil {
			began = o.Status.StartedAt.Time
		}
		switch {
		case o.Status.Usage == nil || began.Before(start) || !began.Before(end):
		case spendNotSettled(&o):
			spend.Add(admitted)
		default:
			spend.Add(quantity(o.Status.Usage.Spend))
		}
	}
	s.Spend, s.Reserved = decimal(spend), decimal(reserved)
}

// writeLedger writes the status if it changed. A write that does not change the ledger would start
// a new check of each waiting run, so the controller writes only a change.
func (r *AgentReconciler) writeLedger(ctx context.Context, b *corev1beta1.SpendBudget,
	before *corev1beta1.SpendBudgetStatus, now time.Time) error {
	if equality.Semantic.DeepEqual(before, &b.Status) {
		return nil
	}
	b.Status.ObservedTime = &metav1.Time{Time: now.Truncate(time.Second)}
	return r.Status().Update(ctx, b)
}

// reserve checks the period budget of the namespace before the key of a run exists. With no
// SpendBudget, the run does not wait. If the run must wait, reserve gives the time to check again
// and the reason for the Agent condition.
func (r *AgentReconciler) reserve(ctx context.Context, a *corev1beta1.Agent) (time.Duration, string, error) {
	b, err := r.spendBudget(ctx, a.Namespace)
	if b == nil || err != nil {
		return 0, "", err
	}
	now := r.now()
	before := b.Status.DeepCopy()
	if err := r.roll(ctx, b, now); err != nil {
		return 0, "", err
	}
	if i := reservation(b, a.UID); i >= 0 {
		if b.Status.Reservations[i].Budget == decimal(a.Spec.Budget) {
			return 0, "", r.writeLedger(ctx, b, before, now)
		}
		// The run budget changed before the key exists. Drop the old reservation and check the new
		// budget, because the key gets the new budget.
		held, reserved := quantity(b.Status.Reservations[i].Budget), quantity(b.Status.Reserved)
		reserved.Sub(held)
		if reserved.Sign() < 0 {
			reserved = resource.Quantity{}
		}
		b.Status.Reserved = decimal(reserved)
		b.Status.Reservations = slices.Delete(b.Status.Reservations, i, i+1)
	}
	waiting, err := r.waitingRuns(ctx, a.Namespace, b)
	if err != nil {
		return 0, "", err
	}
	limit, budget := b.Spec.Spend, a.Spec.Budget
	spend, reserved := quantity(b.Status.Spend), quantity(b.Status.Reserved)
	total := spend.DeepCopy()
	total.Add(reserved)
	total.Add(budget)
	older := slices.ContainsFunc(waiting, func(o corev1beta1.Agent) bool {
		return o.UID != a.UID && olderFirst(o, *a) < 0 && o.Spec.Budget.Cmp(limit) <= 0
	})
	end := b.Status.PeriodEnd.UTC().Format(time.RFC3339)
	var msg string
	switch {
	case budget.Cmp(limit) > 0:
		msg = fmt.Sprintf("The run budget %s is more than the limit %s of the SpendBudget %s.",
			decimal(budget), decimal(limit), b.Name)
	case older:
		msg = fmt.Sprintf("An older run waits for the SpendBudget %s. The runs start in the order of their "+
			"creation time.", b.Name)
	case total.Cmp(limit) > 0:
		msg = fmt.Sprintf("The spend %s and the reserved budgets %s of the period, with the run budget %s, are "+
			"more than the limit %s of the SpendBudget %s. The period ends at %s.", decimal(spend),
			decimal(reserved), decimal(budget), decimal(limit), b.Name, end)
	case len(b.Status.Reservations) >= maxReservations:
		msg = fmt.Sprintf("The SpendBudget %s has the maximum of %d active runs.", b.Name, maxReservations)
	}
	count := len(slices.DeleteFunc(waiting, func(o corev1beta1.Agent) bool { return o.UID == a.UID }))
	// The condition message is the same for each waiting run. A message for each run would change
	// the status at each check, and each change starts a new check of all the waiting runs. The Agent
	// condition has the message for the run.
	cond := metav1.Condition{Type: corev1beta1.SpendBudgetExceeded, Status: metav1.ConditionFalse,
		Reason: "WithinLimit", Message: "The last run started in the limit.", ObservedGeneration: b.Generation}
	if msg != "" {
		count++
		cond.Status, cond.Reason = metav1.ConditionTrue, corev1beta1.ReasonPeriodBudgetExceeded
		cond.Message = fmt.Sprintf("At least one run waits for the budget. The period ends at %s.", end)
	} else {
		b.Status.Reservations = append(b.Status.Reservations, corev1beta1.SpendReservation{
			RunID: string(a.UID), Agent: a.Name, Budget: decimal(budget)})
		reserved.Add(budget)
		b.Status.Reserved = decimal(reserved)
	}
	b.Status.WaitingRuns = int32(count)
	meta.SetStatusCondition(&b.Status.Conditions, cond)
	if err := r.writeLedger(ctx, b, before, now); err != nil {
		return 0, "", err
	}
	if msg == "" {
		return 0, "", nil
	}
	return max(time.Second, min(b.Status.PeriodEnd.Sub(now), r.budgetRetry())), msg, nil
}

// budgetWait is the result of provision when the run waits for the budget. Reconcile requeues the
// Agent after the time, and the run does not fail.
type budgetWait struct{ after time.Duration }

func (w budgetWait) Error() string { return "the run waits for the period budget" }

// waitForBudget keeps the run Pending with the reason PeriodBudgetExceeded. It writes the Agent
// status only if it changed, because each write starts a new reconcile.
func (r *AgentReconciler) waitForBudget(ctx context.Context, a *corev1beta1.Agent, after time.Duration,
	msg string) error {
	before := a.Status.DeepCopy()
	a.Status.Phase = corev1beta1.AgentPending
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: condProvisioned, Status: metav1.ConditionFalse, Reason: corev1beta1.ReasonPeriodBudgetExceeded,
		Message: msg, ObservedGeneration: a.Generation,
	})
	if !equality.Semantic.DeepEqual(before, &a.Status) {
		logf.FromContext(ctx).Info("The run waits for the period budget", "reason", msg)
		if err := r.Status().Update(ctx, a); err != nil {
			return err
		}
	}
	return budgetWait{after: after}
}

// settle moves a run from reserved to spend, with the settled spend of the run. If the spend did not
// settle, the usage can be low, so settle counts the full run budget: the gateway stops the run at
// that budget. It does nothing if the ledger has no reservation for the run, for example after the
// end of the period.
func (r *AgentReconciler) settle(ctx context.Context, a *corev1beta1.Agent) error {
	b, err := r.spendBudget(ctx, a.Namespace)
	if b == nil || err != nil {
		return err
	}
	i := reservation(b, a.UID)
	if i < 0 {
		return nil
	}
	before := b.Status.DeepCopy()
	held := quantity(b.Status.Reservations[i].Budget)
	reserved := quantity(b.Status.Reserved)
	reserved.Sub(held)
	if reserved.Sign() < 0 {
		reserved = resource.Quantity{}
	}
	// With no usage, the run had no key at the gateway, so it has no spend.
	var used resource.Quantity
	if a.Status.Usage != nil {
		if used, err = resource.ParseQuantity(a.Status.Usage.Spend); err != nil || spendNotSettled(a) {
			used = held
		}
	}
	spend := quantity(b.Status.Spend)
	spend.Add(used)
	b.Status.Spend, b.Status.Reserved = decimal(spend), decimal(reserved)
	b.Status.Reservations = slices.Delete(b.Status.Reservations, i, i+1)
	return r.writeLedger(ctx, b, before, r.now())
}

// reasonKeyBeforeLedger is the reason for a run that got its key hash from a version before the
// ledger, in a namespace with a SpendBudget.
const reasonKeyBeforeLedger = "KeyBeforeLedger"

// admitKeyBeforeLedger handles a Pending run with a key hash from a version before the ledger. Its
// key can exist at the gateway with a budget that the ledger does not know, and a new CreateKey does
// not change the budget of a key that exists. Thus in a namespace with a SpendBudget, the run fails
// and the revoke blocks the key. With no SpendBudget, the key gets spec.budget, as before.
func (r *AgentReconciler) admitKeyBeforeLedger(ctx context.Context, a *corev1beta1.Agent) (bool, error) {
	b, err := r.spendBudget(ctx, a.Namespace)
	if err != nil {
		return false, err
	}
	if b == nil {
		a.Status.Budget = decimal(a.Spec.Budget)
		return false, r.Status().Update(ctx, a)
	}
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: condProvisioned, Status: metav1.ConditionFalse, Reason: reasonKeyBeforeLedger,
		Message:            "The key of the run is from a version before the period budget ledger. Create the Agent again.",
		ObservedGeneration: a.Generation,
	})
	return true, r.fail(ctx, a, corev1beta1.AgentFailed, reasonKeyBeforeLedger)
}

// admitted is true if the ledger admitted the run. A key hash with no status.budget is from a version
// before the ledger.
func admitted(a *corev1beta1.Agent) bool {
	return a.Status.KeyHash != "" && a.Status.Budget != ""
}

// admittedBudget is the run budget that the key got. An Agent from a version before status.budget
// has only spec.budget.
func admittedBudget(a *corev1beta1.Agent) resource.Quantity {
	q, err := resource.ParseQuantity(a.Status.Budget)
	if err != nil {
		return a.Spec.Budget
	}
	return q
}

// spendNotSettled is true if the revoke found that the spend of the run did not settle.
func spendNotSettled(a *corev1beta1.Agent) bool {
	c := meta.FindStatusCondition(a.Status.Conditions, condRevoked)
	return c != nil && strings.HasSuffix(c.Message, spendNotSettledNote)
}

// waitingFor maps a change of a SpendBudget to the runs that wait for it.
func (r *AgentReconciler) waitingFor(ctx context.Context, o client.Object) []reconcile.Request {
	b, ok := o.(*corev1beta1.SpendBudget)
	if !ok {
		return nil
	}
	runs, err := r.waitingRuns(ctx, b.Namespace, b)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Cannot list the runs that wait for the SpendBudget")
		return nil
	}
	out := make([]reconcile.Request, 0, len(runs))
	for _, a := range runs {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&a)})
	}
	return out
}
