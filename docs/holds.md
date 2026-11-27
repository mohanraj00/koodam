# Holds

A `Hold` is an incident control on a workload. It has 2 actions:

- `Stop`: the kill switch. The controller blocks the gateway key of each workload of the target, and then it stops the pods.
- `Quarantine`: the controller blocks the gateway key, and the egress proxy refuses each call of the pods. The pods and the PVCs stay for an investigation.

This change adds only the API, the CRD rules, the roles and the admission policy. No controller acts on a Hold yet. The stop and the quarantine come in the next changes of [#185](https://github.com/mohanraj00/koodam/issues/185).

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
