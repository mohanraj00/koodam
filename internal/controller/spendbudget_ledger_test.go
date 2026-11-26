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
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// The run names of the ledger tests.
const (
	ledgerRun1   = "run-1"
	ledgerRun2   = "run-2"
	ledgerHuge   = "huge"
	ledgerGone   = "gone"
	ledgerSmall  = "small"
	ledgerOlder  = "older"
	ledgerNext   = "next"
	ledgerLater  = "later"
	ledgerActive = "active"
	ledgerBig    = "big"
	// ledgerJob1 is the Job of the run ledgerRun1.
	ledgerJob1 = "kd-run-1"
)

func TestBudgetPeriod(t *testing.T) {
	at := time.Date(2026, time.December, 31, 23, 59, 0, 0, time.FixedZone("x", 2*3600))
	for _, c := range []struct {
		period     corev1alpha1.SpendPeriod
		start, end time.Time
	}{
		{corev1alpha1.SpendPeriodDay, time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{corev1alpha1.SpendPeriodMonth, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		start, end := budgetPeriod(c.period, at)
		if !start.Equal(c.start) || !end.Equal(c.end) {
			t.Errorf("%s: got %v to %v, want %v to %v", c.period, start, end, c.start, c.end)
		}
	}
	for in, want := range map[string]string{"6": "6", "6.250": "6.25", "0.000": "0", "2.50": "2.5"} {
		if got := decimal(resource.MustParse(in)); got != want {
			t.Errorf("decimal(%s) = %s, want %s", in, got, want)
		}
	}
}

// A rebuild past the cap of reservations keeps the budget of each active run in reserved.
func TestRebuildLedgerCap(t *testing.T) {
	start, end := budgetPeriod(corev1alpha1.SpendPeriodDay, time.Date(2026, time.November, 25, 10, 0, 0, 0, time.UTC))
	budgets := []string{"1", "2", "3"}
	runs := make([]corev1alpha1.Agent, 0, len(budgets))
	for i, b := range budgets {
		a := corev1alpha1.Agent{}
		a.Name, a.UID = fmt.Sprintf("run-%d", i), types.UID(fmt.Sprintf("uid-%d", i))
		a.Status.KeyHash, a.Status.Budget = "hash", b
		runs = append(runs, a)
	}
	s := &corev1alpha1.SpendBudgetStatus{}
	rebuildLedger(s, runs, start, end, resource.MustParse("10"), 2)
	if len(s.Reservations) != 2 || s.Reserved != "6" {
		t.Errorf("reservations %d, reserved %s; want 2 and 6", len(s.Reservations), s.Reserved)
	}
}

// staleAgents is a cache that has not seen the keys of the Agents.
type staleAgents struct{ client.Client }

func (c staleAgents) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if l, ok := list.(*corev1alpha1.AgentList); ok {
		for i := range l.Items {
			l.Items[i].Status.KeyHash = ""
		}
	}
	return nil
}

// The ledger of a SpendBudget. The tests call Reconcile with a fake gateway and a fake clock.
var _ = Describe("SpendBudget ledger", func() {
	var (
		ctx   context.Context
		ns    string
		gw    *fakeGateway
		clock time.Time
		r     *AgentReconciler
	)
	day1 := time.Date(2026, time.November, 25, 10, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		ctx = context.Background()
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ledger-"}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, n))).To(Succeed()) })
		ns, clock = n.Name, day1
		gw = &fakeGateway{runBinding: types.NamespacedName{Namespace: ns, Name: "no-binding"}}
		r = &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Gateway: gw,
			GatewayURL: testGatewayURL, Grace: time.Minute, JobTTL: 3600, Now: func() time.Time { return clock },
			BudgetRetry: 30 * time.Second}
		Expect(k8sClient.Create(ctx, &corev1alpha1.SpendBudget{
			ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.SpendBudgetName, Namespace: ns},
			Spec:       corev1alpha1.SpendBudgetSpec{PeriodLimits: corev1alpha1.PeriodLimits{Spend: resource.MustParse("10")}},
		})).To(Succeed())
	})

	// newRun makes a run. The creation time has a resolution of one second, and the name decides the
	// order of runs of the same second. Thus each test makes its runs in the order of their names.
	newRun := func(name, budget string) {
		Expect(k8sClient.Create(ctx, &corev1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AgentSpec{Image: "registry.k8s.io/pause:3.9", Command: []string{"/ledger"},
				Model: "ledger-model", Budget: resource.MustParse(budget), DeadlineSeconds: 600},
		})).To(Succeed())
	}
	reconcileRun := func(name string) reconcile.Result {
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	run := func(name string) *corev1alpha1.Agent {
		a := &corev1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, a)).To(Succeed())
		return a
	}
	budget := func() *corev1alpha1.SpendBudget {
		b := &corev1alpha1.SpendBudget{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: corev1alpha1.SpendBudgetName}, b)).To(Succeed())
		return b
	}
	expectWaits := func(name string, res reconcile.Result) {
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", 30*time.Second))
		a := run(name)
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentPending))
		Expect(a.Status.KeyHash).To(BeEmpty())
		c := meta.FindStatusCondition(a.Status.Conditions, condProvisioned)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(corev1alpha1.ReasonPeriodBudgetExceeded))
	}

	It("holds a run until the next period when the period limit has no room", func() {
		newRun(ledgerRun1, "6")
		newRun(ledgerRun2, "6")

		By("run 1 starts and reserves its budget")
		reconcileRun(ledgerRun1)
		Expect(run(ledgerRun1).Status.Phase).To(Equal(corev1alpha1.AgentRunning))
		b := budget()
		Expect(b.Status.Reserved).To(Equal("6"))
		Expect(b.Status.Reservations).To(HaveLen(1))
		Expect(b.Status.PeriodStart.Time).To(BeTemporally("==", time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)))

		By("run 2 waits, because 6 + 6 is more than 10")
		expectWaits(ledgerRun2, reconcileRun(ledgerRun2))
		b = budget()
		Expect(b.Status.WaitingRuns).To(Equal(int32(1)))
		Expect(meta.IsStatusConditionTrue(b.Status.Conditions, corev1alpha1.SpendBudgetExceeded)).To(BeTrue())
		Expect(gw.created).To(HaveLen(1))

		By("run 1 ends with a spend of 6, and run 2 still waits")
		gw.spend = 6
		endRunJob(ctx, types.NamespacedName{Namespace: ns, Name: ledgerJob1}, batchv1.JobComplete)
		reconcileRun(ledgerRun1)
		Expect(run(ledgerRun1).Status.Phase).To(Equal(corev1alpha1.AgentSucceeded))
		b = budget()
		Expect(b.Status.Spend).To(Equal("6"))
		Expect(b.Status.Reserved).To(Equal("0"))
		Expect(b.Status.Reservations).To(BeEmpty())
		Expect(r.waitingFor(ctx, b)).To(ConsistOf(reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: ledgerRun2}}))
		expectWaits(ledgerRun2, reconcileRun(ledgerRun2))

		By("the clock goes to the next day, and run 2 starts")
		clock = day1.Add(24 * time.Hour)
		reconcileRun(ledgerRun2)
		Expect(run(ledgerRun2).Status.Phase).To(Equal(corev1alpha1.AgentRunning))
		b = budget()
		Expect(b.Status.PeriodStart.Time).To(BeTemporally("==", time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)))
		Expect(b.Status.Spend).To(Equal("0"))
		Expect(b.Status.Reserved).To(Equal("6"))
		Expect(b.Status.WaitingRuns).To(BeZero())
	})

	It("starts the waiting runs in the order of their creation time", func() {
		newRun(ledgerBig, "8")
		newRun(ledgerOlder, "6")
		newRun(ledgerSmall, "1")
		reconcileRun(ledgerBig)
		expectWaits(ledgerOlder, reconcileRun(ledgerOlder))
		// The small run fits (8 + 1), but an older run waits.
		expectWaits(ledgerSmall, reconcileRun(ledgerSmall))
		Expect(budget().Status.WaitingRuns).To(Equal(int32(2)))
		Expect(r.waitingFor(ctx, budget())).To(HaveLen(2))
	})

	It("writes no SpendBudget status when two waiting runs check the budget again", func() {
		newRun(ledgerBig, "8")
		newRun(ledgerOlder, "6")
		newRun(ledgerSmall, "1")
		reconcileRun(ledgerBig)
		expectWaits(ledgerOlder, reconcileRun(ledgerOlder))
		expectWaits(ledgerSmall, reconcileRun(ledgerSmall))
		expectWaits(ledgerOlder, reconcileRun(ledgerOlder))
		version := budget().ResourceVersion
		expectWaits(ledgerSmall, reconcileRun(ledgerSmall))
		expectWaits(ledgerOlder, reconcileRun(ledgerOlder))
		Expect(budget().ResourceVersion).To(Equal(version))
		Expect(meta.FindStatusCondition(budget().Status.Conditions, corev1alpha1.SpendBudgetExceeded).Message).
			To(Equal("At least one run waits for the budget. The period ends at 2026-11-26T00:00:00Z."))
	})

	It("counts the full run budget when the spend did not settle", func() {
		newRun(ledgerRun1, "6")
		reconcileRun(ledgerRun1)
		gw.usageErr = gateway.ErrSpendNotSettled
		endRunJob(ctx, types.NamespacedName{Namespace: ns, Name: ledgerJob1}, batchv1.JobComplete)
		reconcileRun(ledgerRun1)
		Expect(meta.FindStatusCondition(run(ledgerRun1).Status.Conditions, condRevoked).Message).
			To(HaveSuffix(spendNotSettledNote))
		b := budget()
		Expect(b.Status.Spend).To(Equal("6"))
		Expect(b.Status.Reserved).To(Equal("0"))
	})

	It("rebuilds the ledger when the period changes after the old period ended", func() {
		newRun(ledgerRun1, "6")
		reconcileRun(ledgerRun1)
		gw.spend = 6
		endRunJob(ctx, types.NamespacedName{Namespace: ns, Name: ledgerJob1}, batchv1.JobComplete)
		reconcileRun(ledgerRun1)
		Expect(budget().Status.Spend).To(Equal("6"))
		// The run Job starts at the wall clock time. The rebuild counts the run on the fake day.
		a := run(ledgerRun1)
		a.Status.StartedAt = &metav1.Time{Time: day1}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())

		By("the next day, the budget changes from Day to Month")
		clock = day1.Add(24 * time.Hour)
		b := budget()
		b.Spec.Period = corev1alpha1.SpendPeriodMonth
		Expect(k8sClient.Update(ctx, b)).To(Succeed())
		newRun(ledgerRun2, "6")
		expectWaits(ledgerRun2, reconcileRun(ledgerRun2))
		b = budget()
		Expect(b.Status.PeriodStart.Time).To(BeTemporally("==", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))
		Expect(b.Status.Spend).To(Equal("6"))
	})

	It("keeps the spend of a deleted run when the period changes", func() {
		newRun(ledgerRun1, "6")
		reconcileRun(ledgerRun1)
		gw.spend = 6
		endRunJob(ctx, types.NamespacedName{Namespace: ns, Name: ledgerJob1}, batchv1.JobComplete)
		reconcileRun(ledgerRun1)
		Expect(budget().Status.Spend).To(Equal("6"))
		Expect(k8sClient.Delete(ctx, run(ledgerRun1))).To(Succeed())
		reconcileRun(ledgerRun1)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: ledgerRun1},
			&corev1alpha1.Agent{}))).To(BeTrue())

		By("the same day, the budget changes from Day to Month")
		b := budget()
		b.Spec.Period = corev1alpha1.SpendPeriodMonth
		Expect(k8sClient.Update(ctx, b)).To(Succeed())
		newRun(ledgerRun2, "6")
		expectWaits(ledgerRun2, reconcileRun(ledgerRun2))
		Expect(budget().Status.Spend).To(Equal("6"))
	})

	It("fails a run with a budget of zero or less, and reserves nothing", func() {
		newRun(ledgerRun1, "-5000")
		newRun(ledgerRun2, "0")
		reconcileRun(ledgerRun1)
		reconcileRun(ledgerRun2)
		for _, name := range []string{ledgerRun1, ledgerRun2} {
			a := run(name)
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
			Expect(meta.FindStatusCondition(a.Status.Conditions, condProvisioned).Message).
				To(Equal("spec.budget must be more than zero."))
		}
		Expect(budget().Status.Reservations).To(BeEmpty())
		Expect(gw.created).To(BeEmpty())
	})

	It("checks the budget again if it changed after the reservation and before the key", func() {
		newRun(ledgerActive, "4")
		reconcileRun(ledgerActive)
		newRun(ledgerRun1, "1")
		// A crash after the reservation and before the key hash leaves a reservation with no key.
		after, _, err := r.reserve(ctx, run(ledgerRun1))
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(BeZero())
		Expect(budget().Status.Reserved).To(Equal("5"))
		a := run(ledgerRun1)
		a.Spec.Budget = resource.MustParse("8")
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		expectWaits(ledgerRun1, reconcileRun(ledgerRun1))
		b := budget()
		Expect(b.Status.Reserved).To(Equal("4"))
		Expect(b.Status.Reservations).To(HaveLen(1))
		Expect(gw.created).To(HaveLen(1))
	})

	It("keeps the reservation of an ended run that is not revoked when it rebuilds the ledger", func() {
		newRun(ledgerActive, "4")
		reconcileRun(ledgerActive)
		a := run(ledgerActive)
		a.Status.Phase = corev1alpha1.AgentFailed
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		newRun(ledgerLater, "7")
		expectWaits(ledgerLater, reconcileRun(ledgerLater))
		b = budget()
		Expect(b.Status.Reserved).To(Equal("4"))
		Expect(b.Status.Reservations).To(HaveLen(1))
	})

	It("gives a retried key the reserved budget after spec.budget changed", func() {
		newRun(ledgerRun1, "1")
		gw.createErr = errors.New("the gateway is not available")
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: ledgerRun1}})
		Expect(err).To(HaveOccurred())
		a := run(ledgerRun1)
		Expect(a.Status.KeyHash).NotTo(BeEmpty())
		Expect(a.Status.Budget).To(Equal("1"))
		a.Spec.Budget = resource.MustParse("10")
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		gw.createErr = nil
		reconcileRun(ledgerRun1)
		Expect(gw.created).To(HaveLen(1))
		Expect(gw.created[0].Budget).To(Equal(1.0))
		Expect(budget().Status.Reserved).To(Equal("1"))
	})

	It("fails a run whose key hash is from a version before the ledger", func() {
		newRun(ledgerRun1, "8")
		gw.createErr = errors.New("the gateway is down")
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: ledgerRun1}})
		Expect(err).To(HaveOccurred())
		gw.createErr = nil
		a := run(ledgerRun1)
		hash := a.Status.KeyHash
		Expect(hash).NotTo(BeEmpty())
		a.Status.Budget = ""
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		a = run(ledgerRun1)
		a.Spec.Budget = resource.MustParse("1")
		Expect(k8sClient.Update(ctx, a)).To(Succeed())

		reconcileRun(ledgerRun1)
		a = run(ledgerRun1)
		Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
		Expect(meta.FindStatusCondition(a.Status.Conditions, condProvisioned).Reason).To(Equal(reasonKeyBeforeLedger))
		Expect(gw.created).To(BeEmpty())
		Expect(gw.blocked).To(ContainElement(hash))
	})

	It("gives a key hash from a version before the ledger spec.budget with no SpendBudget", func() {
		Expect(k8sClient.Delete(ctx, budget())).To(Succeed())
		newRun(ledgerRun1, "8")
		gw.createErr = errors.New("the gateway is down")
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: ledgerRun1}})
		Expect(err).To(HaveOccurred())
		gw.createErr = nil
		a := run(ledgerRun1)
		a.Status.Budget = ""
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		reconcileRun(ledgerRun1)
		Expect(run(ledgerRun1).Status.Budget).To(Equal("8"))
		Expect(gw.created).To(HaveLen(1))
	})

	It("rebuilds a reservation with the budget that the key got", func() {
		newRun(ledgerActive, "8")
		reconcileRun(ledgerActive)
		a := run(ledgerActive)
		Expect(a.Status.Budget).To(Equal("8"))
		a.Spec.Budget = resource.MustParse("1")
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		newRun(ledgerLater, "7")
		expectWaits(ledgerLater, reconcileRun(ledgerLater))
		Expect(budget().Status.Reserved).To(Equal("8"))
	})

	It("rebuilds the full run budget of a run whose spend did not settle", func() {
		newRun(ledgerRun1, "6")
		reconcileRun(ledgerRun1)
		gw.usageErr = gateway.ErrSpendNotSettled
		endRunJob(ctx, types.NamespacedName{Namespace: ns, Name: ledgerJob1}, batchv1.JobComplete)
		reconcileRun(ledgerRun1)
		a := run(ledgerRun1)
		a.Status.StartedAt = &metav1.Time{Time: day1}
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		newRun(ledgerRun2, "6")
		expectWaits(ledgerRun2, reconcileRun(ledgerRun2))
		Expect(budget().Status.Spend).To(Equal("6"))
	})

	It("does not hold the other runs behind a run with a budget above the limit", func() {
		newRun(ledgerHuge, "11")
		newRun(ledgerNext, "2")
		expectWaits(ledgerHuge, reconcileRun(ledgerHuge))
		Expect(meta.FindStatusCondition(run(ledgerHuge).Status.Conditions, condProvisioned).Message).
			To(ContainSubstring("more than the limit"))
		reconcileRun(ledgerNext)
		Expect(run(ledgerNext).Status.Phase).To(Equal(corev1alpha1.AgentRunning))
	})

	It("releases the reservation of a deleted run", func() {
		newRun(ledgerGone, "6")
		reconcileRun(ledgerGone)
		Expect(budget().Status.Reserved).To(Equal("6"))
		Expect(k8sClient.Delete(ctx, run(ledgerGone))).To(Succeed())
		reconcileRun(ledgerGone)
		b := budget()
		Expect(b.Status.Reserved).To(Equal("0"))
		Expect(b.Status.Spend).To(Equal("0.25"))
	})

	It("rebuilds a run with a key and no status.budget with the limit", func() {
		newRun(ledgerActive, "6")
		reconcileRun(ledgerActive)
		a := run(ledgerActive)
		a.Status.Budget = ""
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		a = run(ledgerActive)
		a.Spec.Budget = resource.MustParse("1")
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		newRun(ledgerLater, "1")
		expectWaits(ledgerLater, reconcileRun(ledgerLater))
		Expect(budget().Status.Reserved).To(Equal("10"))
	})

	It("rebuilds a lost ledger from the API server, not from the cache", func() {
		newRun(ledgerActive, "6")
		reconcileRun(ledgerActive)
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		r.Client = staleAgents{k8sClient}
		newRun(ledgerLater, "6")
		expectWaits(ledgerLater, reconcileRun(ledgerLater))
		Expect(budget().Status.Reserved).To(Equal("6"))
	})

	It("rebuilds a lost ledger from the active runs", func() {
		newRun(ledgerActive, "4")
		reconcileRun(ledgerActive)
		b := budget()
		b.Status = corev1alpha1.SpendBudgetStatus{}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		newRun(ledgerLater, "7")
		expectWaits(ledgerLater, reconcileRun(ledgerLater))
		b = budget()
		Expect(b.Status.Reserved).To(Equal("4"))
		Expect(b.Status.Reservations).To(HaveLen(1))
		Expect(b.Status.Reservations[0].Agent).To(Equal(ledgerActive))
	})
})
