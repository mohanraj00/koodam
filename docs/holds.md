# Holds

A `Hold` is an incident control on a workload. It has 2 actions:

- `Stop`: the kill switch. The controller blocks the gateway key of each workload of the target, and then it stops the pods.
- `Quarantine`: the controller blocks the gateway key, and the egress proxy refuses each call of the pods. The pods and the PVCs stay for an investigation.

The controller does the `Stop` action. For the `Quarantine` action, only the egress proxy acts in this version: it refuses each call of the pods (see [The quarantine](#the-quarantine)). The controller does not block the key and does not change the workload. The Hold shows the reason `Unsupported`. A later change of [#185](https://github.com/mohanraj00/koodam/issues/185) adds the controller part.

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

An App and an AppVersion stop only if the App controllers run. If they do not run, the Hold shows the reason `AppsDisabled`.

With two installs in one cluster (see [zones](zones.md)), each manager stops the workloads of its own namespaces only. A Profile can bind namespaces of the two installs. Then each manager counts the workloads of all the bound namespaces, so the two managers write the same status. The status of a Hold on one workload or on one App comes from the install of its namespace.

## The quarantine

The [egress proxy](traced-egress.md) watches the Holds and the Profiles. While a `Quarantine` Hold has no release, the proxy refuses each call of each pod of the target with the reason `quarantined`. Each refused call has a span with `result: denied` and `reason: quarantined`. When the quarantine starts, the proxy also ends the open calls and the open tunnels of these pods. The proxy reads the Hold and the Profile status from the API server, not from a pod label, so the owner of the workload cannot remove the control.

| Target | The pods that the proxy refuses |
|---|---|
| Agent | The pods of the runs of the Agent. |
| Workspace | The pod of the Workspace. |
| AppVersion | The pods of the version. |
| App | The pods of each version of the App. |
| Profile | The pods of each Agent, Workspace and AppVersion in each namespace that the Profile binds. The proxy reads the bound namespaces from `status.boundNamespaces` of the Profile. |

The proxy writes a span of the kind `control` when a quarantine starts and when it ends. The field `by` has `spec.user` at the start and `spec.release.user` at the end. A Profile target writes 1 control span for each bound namespace. See [egress spans](egress-spans.md).

In this version, the quarantine blocks only the egress through the proxy. The gateway key stays active, and the pods stay. The proxy sees only the calls that go through it. A call in a path with no proxy, for example to a host in `workspace.networkPolicy.extraEgress` or in a namespace that the NetworkPolicy does not cover, has no check.

### Failure modes and limits

- At the start, the proxy is not ready until it reads the pods, the Holds and the Profiles. If it cannot read them in the time limit of `--sync-timeout`, the proxy exits, and Kubernetes starts it again. Thus a new proxy pod accepts no call before it knows each active quarantine.
- After the start, if the watch of the Holds or the Profiles fails, the proxy keeps the last state that it read, and the client tries the watch again. A new Hold or a release can then come late. The tenant keys of the proxy use the same mode. The time of the gap is the time until the watch works again.
- After a restart, the proxy writes the `quarantined` control span of each active quarantine again. A reader must accept more than 1 start span for one Hold.
- A Hold that is deleted with no release writes no `released` span. The admission policy refuses this delete.

### Known limit

If `apps.enabled=false` and the pods of an old AppVersion still run, a Profile Hold does not stop them. The proxy still refuses the calls of these pods during a quarantine. Without the App controllers, the controller cannot read the AppVersions, so the Hold can show `Held=True` while these pods run. Before you disable the App controllers, retire each AppVersion. A later change fixes this limit.

### The status of the Hold

The controller writes the condition `Held` on the Hold:

| Status | Reason | Meaning |
|---|---|---|
| `False` | `Stopping` | Some workloads of the target did not stop yet. The message gives the number. |
| `True` | `Stopped` | Each workload of the target stopped, or the target has no workload. |
| `False` | `Released` | The Hold has `spec.release`. |
| `False` | `Unsupported` | The action is `Quarantine`. The egress proxy refuses the calls of the pods, and the controller does not change the workload. |
| `False` | `AppsDisabled` | The target is an App or an AppVersion, and the App controllers do not run. |

### Events

The controller writes an `events.k8s.io/v1` event:

- a `Warning` event with the reason `Stopped` on each workload that it stops, with the Hold as the related object;
- a `Normal` event with the reason `Stopped` or `Released` on the Hold.

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

## Roles

- `hold-admin-role` in `config/rbac` gives full access to the Holds. Bind it only to the cluster admins who stop or quarantine a workload.
- `hold-viewer-role`, and `<release>-hold-viewer` in the chart, give `get`, `list` and `watch`. Bind them with a ClusterRoleBinding.

The chart has no write role for Holds, and no role of the chart has an aggregate label.
