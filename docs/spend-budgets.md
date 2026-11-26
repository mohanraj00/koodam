# Spend budgets

A `SpendBudget` gives one namespace a spend limit for each period. It is for the Agent runs of the namespace. Each run keeps its own `spec.budget`, which the gateway applies to the key of the run.

## What is active

- The `SpendBudget` API is in `main`, and the chart installs its CRD.
- The Agent controller keeps the ledger in the status, and a run waits when the period has no room. See "The ledger".
- The spend API does not show the budget yet. That comes in a later change of [#128](https://github.com/mohanraj00/koodam/issues/128).

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

The Agent controller writes the status. It has these fields:

- `periodStart` and `periodEnd`: the current period.
- `spend`: the settled spend of the runs that started in the period and ended.
- `reserved`: the sum of the budgets of the active runs of the period.
- `reservations`: one item for each active run, with its UID, its name and its budget. The list has a maximum of 500 items.
- `waitingRuns`: the count of the runs that waited for the budget at the last check.
- The conditions `Ready` and `BudgetExceeded`. `BudgetExceeded` is `True` with the reason `PeriodBudgetExceeded` when the last run that the controller checked had to wait.

## The ledger

The controller counts only the Agent runs. Workspace, AppVersion and TestKey keys keep their own daily budgets.

1. Before the controller makes the key of a run, it reads the SpendBudget of the namespace. With no SpendBudget, the run starts.
2. A run starts only if `spend + reserved + its budget` is not more than `spec.spend`. The controller writes the reservation to the status before it makes the key. The status update with the `resourceVersion` is the lock.
3. A run that cannot start stays `Pending`. Its condition `Provisioned` is `False` with the reason `PeriodBudgetExceeded`, and the message gives the cause and the end of the period. The run does not fail, and the controller makes no key.
4. The controller checks a waiting run again when the SpendBudget changes, at the end of the period, and at the latest after one minute.
5. Waiting runs start in the order of their creation time. A run with a budget that is more than the limit never starts, but it does not stop the runs after it. Delete it, or raise the limit.
6. When a run ends or is deleted, the controller moves its reservation to `spend`, with the settled spend from the gateway. If the spend did not settle in the wait time, the controller counts the full run budget, because the gateway stops the run at that budget. A change of the status starts the check of the waiting runs. The condition `BudgetExceeded` of the SpendBudget has one message for all the waiting runs, so a new check of a run does not change the status.

The rules of the period:

- The spend of a run counts in the period where it started. At the end of a period, the controller drops the reservations of that period. A run of the old period keeps its own budget at the gateway, and its spend does not go into the new period.
- If the status has no period (a new SpendBudget, or a lost status after a restore), or if `spec.period` changes, the controller rebuilds the ledger from the Agents. It finds a change of `spec.period` also after the old period ended. Each active run with a key gets a reservation. Each ended run that started in the period adds its settled spend.
- The ledger has a maximum of 500 active runs. More runs wait.

## Who can write it

Only a cluster admin writes a SpendBudget. A user who can change the limit of a namespace can remove the limit.

- The chart gives the ClusterRole `<release>-spendbudget-viewer`, with `get`, `list` and `watch` only. Bind it to the users of a namespace with a RoleBinding.
- The chart has no write role for SpendBudget. No role of the chart has an aggregate label, so the Kubernetes roles `edit` and `admin` get no SpendBudget rule.
- `config/rbac/spendbudget_admin_role.yaml` is the write role for a kustomize install. Bind it only to cluster admins.

[threat-model.md](threat-model.md) has the threat row T11.
