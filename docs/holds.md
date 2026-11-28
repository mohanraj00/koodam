# Holds

Release: v0.6.0.

A `Hold` is an incident control on a workload. It has 2 actions:

- `Stop`: the kill switch. The controller blocks the gateway key of each workload of the target, and then it stops the pods.
- `Quarantine`: the controller blocks the gateway key, and the egress proxy refuses each call of the pods. The pods and the PVCs stay for an investigation.

A quarantine needs the [egress proxy](traced-egress.md). Without it, the Hold shows the reason `NoEgressProxy`.

## The resource

A Hold is cluster-scoped. Only a cluster admin writes it, so the owner of a workload cannot clear it.

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: Hold
metadata:
  name: team-a-run-1
spec:
  target:
    kind: Agent        # Agent, Workspace, App, AppVersion or Profile
    namespace: team-a  # not permitted for a Profile
    name: run-1
  action: Stop         # Stop or Quarantine
  reason: The run sends data to an unknown host.
  user: alice@example.com
```

- `spec.target` is the workload. A `Profile` target is each Agent, Workspace and AppVersion in the bound namespaces of the Profile. A Profile is cluster-scoped, so a Profile target has no namespace. Each other kind needs a namespace.
- `spec.user` must be your user name, as the API server knows it. Find it with `kubectl auth whoami`.
- `spec.target`, `spec.action`, `spec.reason` and `spec.user` are immutable. To change the action, release the Hold and make a new one.

## The stop

The reconciler of each kind reads the Holds and uses the revoke path that it already has. It always blocks the gateway key before it stops the pods. If the block fails, it tries again and does not stop the pods.

| Kind | Stop | After the release |
|---|---|---|
| Agent | The run ends with the phase `Failed`. The `Revoked` condition has the reason `Stopped`. The controller deletes the Job and the run credentials. | The run stays ended. Start a new run. |
| Workspace | The controller sets the status condition `Held` and scales the StatefulSet to 0. The phase is `Suspended`, and the home PVC stays. The controller does not change `spec.suspend`. | The Workspace starts again with a new gateway key. |
| AppVersion | The controller revokes the version with no drain. The `Revoked` condition has the reason `Stopped`. | The version stays revoked. Make a new version. |
| App | The controller stops each version of the App. | The same as an AppVersion. |
| Profile | The controller stops each Agent, Workspace and AppVersion in each namespace that the Profile binds, also in a namespace that it binds later. | The same as each kind. |

The owner of a workload cannot clear a stop. Only the controller writes the `Held` condition of a Workspace, and a Workspace with this condition gets no gateway key and no test key.

The owner of a workload can remove the owner reference from the Job of an Agent or from the Deployment of an AppVersion. The controller also deletes such an object, but only if it has no controller and it has the labels of the run or of the version. A Job must also have the run ID of the Agent in its pod template. The controller deletes it after it blocks the key, and the garbage collector then deletes its pods. If the owner reference of a pod is also removed, the pod stays, but its gateway key is blocked.

An App and an AppVersion stop only if the App controllers run. If they do not run, the Hold shows the reason `AppsDisabled`. A Profile Hold also shows `AppsDisabled` if a namespace that the Profile binds has an AppVersion.

With two installs in one cluster (see [zones](zones.md)), each manager stops the workloads of its own namespaces only. A Profile can bind namespaces of the two installs. Then each manager counts the workloads of all the bound namespaces, so the two managers write the same status. The status of a Hold on one workload or on one App comes from the install of its namespace.

## The quarantine

The [egress proxy](traced-egress.md) watches the Holds and the Profiles. While a `Quarantine` Hold has no release, the proxy refuses each call of each pod of the target with the reason `quarantined`. Each refused call has a span with `result: denied` and `reason: quarantined`. When the quarantine starts, the proxy also ends the open calls and the open tunnels of these pods. The proxy reads the Hold and the Profile status from the API server, so the owner of the workload cannot remove the Hold. The proxy finds the pods of a target by the pod labels `koodam.dev/agent`, `koodam.dev/workspace`, `koodam.dev/app` and `koodam.dev/app-version`. An admission policy of the chart makes these labels immutable on a pod. See [The policies of a quarantine](#the-policies-of-a-quarantine).

| Target | The pods that the proxy refuses |
|---|---|
| Agent | The pods of the runs of the Agent. |
| Workspace | The pod of the Workspace. |
| AppVersion | The pods of the version. |
| App | The pods of each version of the App. |
| Profile | The pods of each Agent, Workspace and AppVersion in each namespace that the Profile binds. The proxy reads the bound namespaces from `status.boundNamespaces` of the Profile. |

The proxy writes a span of the kind `control` when a quarantine starts and when it ends. The field `by` has `spec.user` at the start and `spec.release.user` at the end. A Profile target writes 1 control span for each bound namespace. When a Profile binds a new namespace during a quarantine, the proxy writes the start span of that namespace. The proxy writes control spans in the order of its quarantine state changes. For each quarantine, it writes all `quarantined` spans before its first `released` span. See [egress spans](egress-spans.md).

The reconciler of each kind blocks the gateway key and keeps the pods. It adds the finalizer `koodam.dev/quarantine`, so a delete of the workload waits for the release. It also adds the label `koodam.dev/quarantine` to the workload and to its children, and an admission policy of the chart refuses the delete of an object with this label. See [The policies of a quarantine](#the-policies-of-a-quarantine). The release changes the quarantine to a stop. At the release, the controller removes the label first, and then it stops the pods:

| Kind | Quarantine | After the release |
|---|---|---|
| Agent | The key is blocked. The controller removes `activeDeadlineSeconds` from the Job, so the Job controller does not stop the pod at the deadline. The controller does this in each phase of the run. The run stays `Running`, and the status condition `Held` has the reason `Quarantined`. A run that did not start does not start. | The run ends as for a stop. A deleted Agent goes after the stop. |
| Workspace | The key is blocked, and the test keys are blocked. The pod and the home PVC stay. The phase is `Pending`, and the `Ready` condition has the reason `Quarantined`. | The controller stops the pod. When the API server has no pod of the Workspace, the Workspace starts again with a new key and a new pod. A deleted Workspace goes after the release. |
| AppVersion | The key is blocked, and the controller does not renew it. The pods stay. | The controller revokes the version with the reason `Stopped`. The version stays revoked. |
| App | Each version of the App. | The same as an AppVersion. |
| Profile | Each Agent, Workspace and AppVersion in each namespace that the Profile binds. | The same as each kind. |

If a Stop Hold and a Quarantine Hold target the same workload, the quarantine applies first. After its release, the stop applies.

The proxy sees only the calls that go through it. A call in a path with no proxy, for example to a host in `workspace.networkPolicy.extraEgress` or in a namespace that the NetworkPolicy does not cover, has no check.

### Failure modes and limits

- At the start, the proxy is not ready until it reads the pods, the Holds and the Profiles. If it cannot read them in the time limit of `--sync-timeout`, the proxy exits, and Kubernetes starts it again. Thus a new proxy pod accepts no call before it knows each active quarantine.
- After the start, if the watch of the Holds or the Profiles fails, the proxy keeps the last state that it read, and the client tries the watch again. A new Hold or a release can then come late. The tenant keys of the proxy use the same mode. The time of the gap is the time until the watch works again.
- After a restart, the proxy writes the `quarantined` control span of each active quarantine again. A reader must accept more than 1 start span for one Hold.
- A Hold that is deleted with no release writes no `released` span. The admission policy refuses this delete.
- If the delete of an Agent, a Workspace or an AppVersion started before the quarantine, Kubernetes refuses a new finalizer. Then the controller blocks the key and keeps the pods, and the finalizer that the workload already has keeps it until the release. For a Workspace, this is the gateway finalizer `koodam.dev/workspace-gateway`. The controller does not remove it while a `Quarantine` Hold is active, so the Workspace, its StatefulSet and its pod stay. After the release, the delete continues.
- A Workspace with no gateway has no finalizer before the quarantine. If a user deletes it before the controller reconciles the Hold, Kubernetes removes it at once ([#470](https://github.com/mohanraj00/koodam/issues/470)).
- Only the chart installs [the policies of a quarantine](#the-policies-of-a-quarantine). In an install with kustomize, a user who can delete the pods, the Job or the StatefulSet, or who can patch the labels of a pod, can end a quarantine. Limit these rights in the namespace during an investigation.
- A pod that starts during a quarantine gets the label at the next reconcile of its workload. Before that, the policy does not refuse its delete. The proxy refuses its calls from the start.
- The policies refuse a delete. They do not refuse the eviction of a pod, for example at a node drain, or a change of the pod spec that stops a container ([#459](https://github.com/mohanraj00/koodam/issues/459)). Limit the right to evict and to patch pods in the namespace during an investigation, and do not drain the node.
- The delete of a namespace with a quarantined workload waits for the release, because the API server refuses the delete of the pods.

### The status of the Hold

The controller writes the condition `Held` on the Hold:

| Status | Reason | Meaning |
|---|---|---|
| `False` | `Stopping` | Some workloads of the target did not stop yet. The message gives the number. |
| `True` | `Stopped` | Each workload of the target stopped, or the target has no workload. |
| `False` | `Quarantining` | Some workloads of the target are not quarantined yet. The message gives the number. Or a namespace of a Profile target has no record of its install yet. See [A Profile across installs](#a-profile-across-installs). |
| `True` | `Quarantined` | The key of each workload of the target is blocked, and its pods stay. |
| `False` | `NoEgressProxy` | The action is `Quarantine`, and the controller has no `--egress-proxy-ip`. For a Profile target, the install of a bound namespace has no egress proxy, and the message gives the namespaces. No component refuses the calls of the pods. The controllers still block the keys and keep the pods. Use `Stop`, or enable the egress proxy. |
| `False` | `Released` | The Hold has `spec.release`. |
| `False` | `AppsDisabled` | The App controllers do not run, and the target is an App or an AppVersion. Or the target is a Profile, and a namespace that it binds has an AppVersion. No controller stops or quarantines these versions. For a Profile, the controller reads the AppVersions again each minute. Retire each AppVersion, or enable the App controllers. |

### A Profile across installs

A Profile can bind the namespaces of 2 installs (see [zones](zones.md)). The egress proxy of one install does not refuse the calls of the pods of the other install. Thus, for a `Quarantine` Hold on a Profile, the manager of each install writes one record in `status.namespaces` for each bound namespace that it watches:

```yaml
status:
  namespaces:
  - name: team-a
    egressProxy: true
  - name: team-b
    egressProxy: false
```

- The Hold is `Quarantined` only if each bound namespace has a record with `egressProxy: true`.
- If a record has `egressProxy: false`, the Hold shows `NoEgressProxy`. The message gives the namespaces.
- If a bound namespace has no record, the Hold shows `Quarantining`. The manager of that install did not write its record yet, or no manager watches the namespace.

Each manager changes only the records of its own namespaces, and it removes the record of a namespace that the Profile does not bind. The managers read the same records, so they write the same condition. The records stay after the release. A Hold on one workload or on one App has no records, because only the install of its namespace writes its status.

### Events

The controller writes an `events.k8s.io/v1` event:

- a `Warning` event with the reason `Stopped` or `Quarantined` on each workload that it stops or quarantines, with the Hold as the related object;
- a `Normal` event with the reason `Stopped`, `Quarantined` or `Released` on the Hold;
- a `Warning` event with the reason `NoEgressProxy` on the Hold.

The note of the event on a workload has the name of the Hold, `spec.user` and `spec.reason`. Each user who can read the events of the namespace can read this note. Do not put a secret in `spec.reason`.

## Release a Hold

Set `spec.release` with your user name and a reason:

```sh
kubectl patch hold team-a-run-1 --type=merge \
  -p '{"spec":{"release":{"user":"alice@example.com","reason":"The investigation is complete."}}}'
```

The release is immutable after it is set. The Hold stays as the record of the incident. Delete it only after the release. A key that a Hold blocked stays blocked after the release.

## The admission policy

A CRD rule cannot read the request. Thus the `ValidatingAdmissionPolicy` `koodam-hold` in [config/hold/admission/policy.yaml](../config/hold/admission/policy.yaml) does these checks:

- `spec.user` of a new Hold, and `spec.release.user` of a release, must be the user of the request.
- A new Hold cannot have `spec.release`.
- The API server refuses the delete of a Hold with no `spec.release`.

The policy applies to each user, also to a member of `system:masters`. It does not match the status subresource.

The chart always installs the policy, with the name `<release>-hold`. Thus 2 installs in one cluster have 2 policies with the same rules. With kustomize, apply `config/hold/admission` after `config/default`.

### The policies of a quarantine

The chart installs 2 more policies from [charts/koodam/templates/quarantine.yaml](../charts/koodam/templates/quarantine.yaml). While a quarantine is active, the controller adds the label `koodam.dev/quarantine: "true"` to these objects:

| Target | Objects with the label |
|---|---|
| Agent | The Agent, its Job and the pods of the run. |
| Workspace | The Workspace, its StatefulSet, its pod and the home PVC. |
| AppVersion | The AppVersion, its Deployment and its pods. |

The policy `<release>-quarantine` does these checks on an object with the label:

- The API server refuses the delete of a pod, a Job, a StatefulSet, a Deployment or a PVC. This rule also applies to the garbage collector and to the controllers of Kubernetes. Thus a scale to 0, a Job deadline or the delete of an owner does not remove a quarantined pod.
- The API server refuses the delete of an Agent, a Workspace or an AppVersion with `propagationPolicy: Foreground`. A normal delete is permitted, and the finalizer keeps the workload until the release.
- Only the manager service account of the install can add, change or remove the label.
- Only the manager can remove a finalizer of koodam (`koodam.dev/...`) from an Agent, a Workspace or an AppVersion. Thus a user cannot remove the workload and leave its children with the label.

The manager has no exception for a delete, so it cannot delete a child that has the label.

The policy `<release>-pod-labels` refuses an update of a pod that adds, changes or removes one of the labels `koodam.dev/agent`, `koodam.dev/workspace`, `koodam.dev/app` and `koodam.dev/app-version`, if the pod has one of them. It has no exception. Kubernetes sets these labels from the pod template when it makes the pod, and no component changes them after that.

The 2 policies apply to each user, also to a member of `system:masters`. With `zone.namespaces`, they apply only in the namespaces of the install. Thus each of 2 installs in one cluster needs `zone.namespaces` (see [zones](zones.md)). If not, the policy of one install refuses the label of the other manager. To delete a quarantined object, release the Hold.

## Roles

- `hold-admin-role` in `config/rbac` gives full access to the Holds. Bind it only to the cluster admins who stop or quarantine a workload.
- `hold-viewer-role`, and `<release>-hold-viewer` in the chart, give `get`, `list` and `watch`. Bind them with a ClusterRoleBinding.

The chart has no write role for Holds, and no role of the chart has an aggregate label.

## Errors

The condition `Held` gives the state.

| Reason of `Held` | Meaning |
|---|---|
| `Stopping`, then `Stopped` | The stop is in progress, then no workload of the target is left. |
| `Quarantining`, then `Quarantined` | The quarantine is in progress, then it is in effect. |
| `NoEgressProxy` | A quarantine needs the egress proxy. |
| `AppsDisabled` | The App controllers do not run, and the target is an App, an AppVersion, or a Profile with AppVersions. |
| `Unsupported` | This controller version does not know the action. |
| `Released` | `spec.release` is set. |

The API server refuses a change of `spec.target`, `spec.action` or a set `spec.release`, and a target with no namespace for a kind other than `Profile`.
