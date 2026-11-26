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
2. A run starts only if `spend + reserved + its budget` is not more than `spec.spend`. A run with a budget of zero or less fails with the reason `Invalid`, because a negative reservation would add room to the limit. The controller writes the reservation to the status before it makes the key. If the run budget changes before the key exists, the controller checks the new budget again. The status update with the `resourceVersion` is the lock. A Pending run with a key hash and no `status.budget` is from a version before the ledger. Its key can exist at the gateway with a budget that the ledger does not know. Thus the run fails with the reason `KeyBeforeLedger`, and the revoke blocks the key. Create the Agent again. With no SpendBudget, the key gets `spec.budget`.
3. A run that cannot start stays `Pending`. Its condition `Provisioned` is `False` with the reason `PeriodBudgetExceeded`, and the message gives the cause and the end of the period. The run does not fail, and the controller makes no key.
4. The controller checks a waiting run again when the SpendBudget changes, at the end of the period, and at the latest after one minute.
5. Waiting runs start in the order of their creation time. A run with a budget that is more than the limit never starts, but it does not stop the runs after it. Delete it, or raise the limit.
6. When a run ends or is deleted, the controller moves its reservation to `spend`, with the settled spend from the gateway. If the spend did not settle in the wait time, the controller counts the full run budget, because the gateway stops the run at that budget. A change of the status starts the check of the waiting runs. The condition `BudgetExceeded` of the SpendBudget has one message for all the waiting runs, so a new check of a run does not change the status.

The rules of the period:

- The spend of a run counts in the period where it started. At the end of a period, the controller drops the reservations of that period. A run of the old period keeps its own budget at the gateway, and its spend does not go into the new period.
- If the status has no period (a new SpendBudget, or a lost status after a restore), or if `spec.period` changes, the controller rebuilds the ledger from the Agents. It reads the Agents from the API server, not from its cache, because the cache can miss the key of a run that started a moment ago. It finds a change of `spec.period` also after the old period ended. A rebuild cannot see the spend of a deleted run. Thus, if the old period overlaps the new period, the rebuild keeps the old `spend` when it is more than the rebuilt spend. After a change from Month to Day, the spend of the day can then include spend from earlier days of the month. Each active run with a key gets a reservation of `status.budget`, the run budget that its key got. A later change of `spec.budget` does not change the key, so the rebuild does not use it. A run with a key and no valid `status.budget` got its key from a version before the ledger. For that run, the rebuild counts the full limit, so no other run starts until it ends. An ended run keeps its reservation until the revoke blocked its key and recorded its usage. Each ended run that started in the period adds its settled spend, or its full `status.budget` if the spend did not settle.
- The ledger has a maximum of 500 active runs. More runs wait. If a rebuild finds more than 500 active runs, the budget of each other run is in `reserved` with no reservation. The limit check counts that budget until the period ends.

## Known limits

A rebuild of the ledger counts only the Agents that exist. Thus in these cases, the ledger can count less than the real spend of the period:

- An admin changes `spec.period` from Day to Month. The ledger of the month has the spend of the current day and the spend of the Agents that exist. The spend of deleted runs from the earlier days of the month is not in the ledger.
- A restore gives the SpendBudget no status, or a status of an earlier time. The rebuild cannot see the spend of the runs that a user deleted in the period.

In these cases, the spend of the period can go above the limit by the spend of the deleted runs. Do not delete the ended Agents of a period before a change of `spec.period` or a restore. [#430](https://github.com/mohanraj00/koodam/issues/430) tracks a ledger that does not depend on the Agents.

After an upgrade from a version before the ledger, a Pending run that already has a key hash and no `status.budget` fails with the reason `KeyBeforeLedger`. Its key can exist at the gateway with a budget that the ledger does not know, so the controller blocks the key. Create the Agent again.

## Who can write it

Only a cluster admin writes a SpendBudget. A user who can change the limit of a namespace can remove the limit.

- The chart gives the ClusterRole `<release>-spendbudget-viewer`, with `get`, `list` and `watch` only. Bind it to the users of a namespace with a RoleBinding.
- The chart has no write role for SpendBudget. No role of the chart has an aggregate label, so the Kubernetes roles `edit` and `admin` get no SpendBudget rule.
- `config/rbac/spendbudget_admin_role.yaml` is the write role for a kustomize install. Bind it only to cluster admins.

[threat-model.md](threat-model.md) has the threat row T11.
