# Spend budgets

A `SpendBudget` gives one namespace a spend limit for each period. It is for the Agent runs of the namespace. Each run keeps its own `spec.budget`, which the gateway applies to the key of the run.

## What is active

- The `SpendBudget` API is in `main`, and the chart installs its CRD.
- No controller reads the type in this change. Thus no run waits for a budget yet. The ledger and the wait come in a later change of [#128](https://github.com/mohanraj00/koodam/issues/128).

## The spec

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: SpendBudget
metadata:
  name: default
  namespace: team-a
spec:
  period: Day
  spend: "25"
```

- The name must be `default`. Thus a namespace has only one SpendBudget.
- `period` is `Day` (the default) or `Month`. A period starts at 00:00 UTC, on each day or on the first day of each month. These are the UTC dates of the [spend API](spend-api.md).
- `spend` is the limit, in the currency of the gateway price table. It must be more than zero.

## The status

The controller of a later change writes the status. It has these fields:

- `periodStart` and `periodEnd`: the current period.
- `spend`: the settled spend of the runs that started in the period and ended.
- `reserved`: the sum of the budgets of the active runs of the period.
- `reservations`: one item for each active run, with its UID, its name and its budget. The list has a maximum of 500 items.
- `waitingRuns`: the count of the runs that wait for the budget.
- The conditions `Ready` and `BudgetExceeded`.

The design rule is: a run starts only if `spend + reserved + its budget` is not more than `spec.spend`. A run that cannot start stays `Pending`, with the reason `PeriodBudgetExceeded`. It does not fail.

## Who can write it

Only a cluster admin writes a SpendBudget. A user who can change the limit of a namespace can remove the limit.

- The chart gives the ClusterRole `<release>-spendbudget-viewer`, with `get`, `list` and `watch` only. Bind it to the users of a namespace with a RoleBinding.
- The chart has no write role for SpendBudget. No role of the chart has an aggregate label, so the Kubernetes roles `edit` and `admin` get no SpendBudget rule.
- `config/rbac/spendbudget_admin_role.yaml` is the write role for a kustomize install. Bind it only to cluster admins.

[threat-model.md](threat-model.md) has the threat row T11.
