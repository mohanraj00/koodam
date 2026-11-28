# Upgrade to v0.7.0

v0.7.0 adds the API version `core.koodam.dev/v1beta1`. The API server serves `v1alpha1` and `v1beta1`. `v1beta1` is the storage version. `v1alpha1` is deprecated: each `v1alpha1` request gets a warning.

Your `v1alpha1` manifests and clients continue to work. The API server converts each object between the 2 versions.

## What changes

- **The CRDs are chart templates.** Before v0.7.0, the 7 core CRDs were in the `crds/` folder of the chart, and `helm upgrade` did not change them. From v0.7.0, `helm upgrade` changes them. `helm uninstall` keeps them.
- **A conversion webhook.** The chart installs the Deployment `<release>-conversion` with 2 replicas. The API server calls it to convert an Agent, a Workspace or a TestKey. The other kinds have the same schema in the 2 versions, and the API server converts them with no webhook.
- **Less text in `kubectl explain`.** The CRDs of the Agent, the Workspace and the AppVersion have no description text for the Kubernetes pod template fields. `kubectl explain` shows no text for these fields. `kubectl explain pod.spec` has the text. The descriptions of the koodam fields stay.
- **2 fields have a different form in `v1beta1`:**
  - `TestKey.spec.testID` has the name `spec.testRunID`.
  - `spec.harness.plugins` of an Agent and `spec.harnesses[].plugins` of a Workspace are a set of at most 8 preset names. In `v1alpha1`, the only permitted value is `nookku`.

## Step 1: adopt the CRDs

Do this step one time, before `helm upgrade`, for a release that you installed before v0.7.0. It gives the 7 core CRDs to your Helm release. It changes no object and no schema.

Set the release name and the release namespace of your install:

```sh
RELEASE=koodam
NAMESPACE=koodam-system
for crd in agents approvalrecords holds profiles spendbudgets testkeys workspaces; do
  kubectl label crd "$crd.core.koodam.dev" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd.core.koodam.dev" meta.helm.sh/release-name="$RELEASE" \
    meta.helm.sh/release-namespace="$NAMESPACE" --overwrite
done
```

If you do not do this step, the chart refuses the upgrade and names the CRD.

**2 installs in one cluster:** only one install owns the CRDs and the conversion webhook. Do the step for that install. In the other install, set `conversion.enabled` to `false`.

## Step 2: upgrade the chart

Run `helm upgrade` as usual. The chart changes the CRDs and installs the conversion webhook.

- For some seconds after the CRD change, the webhook pods are not ready. During this time, a write of an Agent, a Workspace or a TestKey fails. The controller tries again.
- The chart makes the webhook certificate and keeps it in the Secret `<release>-conversion-tls`. The [chart README](../charts/koodam/README.md#the-webhook-certificate) has the other options.

**Install with kustomize:** `config/default` now has the conversion webhook, and it gets its certificate from cert-manager. Thus cert-manager must be in the cluster before `make deploy`. `make install` applies CRDs that name the webhook Service of `config/default`.

## Step 3: move the stored objects to v1beta1

After the upgrade, the API server writes each new or changed object as `v1beta1`. An object that nobody changes stays as `v1alpha1` in the storage. The script `hack/migrate-storage.sh` writes each object again as `v1beta1`, then sets `status.storedVersions` of each CRD to `v1beta1` only.

Run it one time, as a cluster admin, when the conversion webhook is ready:

```sh
hack/migrate-storage.sh
```

- The script changes no field of an object. A second run makes no change.
- The script needs kubectl 1.24 or later. To use a different command, set `KUBECTL`, for example `KUBECTL="kubectl --context staging"`.
- v0.7.0 works if you do not run the script. But do it soon: after it, the controller reads the stored objects with no conversion. The script is mandatory before the release that removes `v1alpha1`.

## If the conversion webhook is not ready

A CRD conversion has no failure policy. If the API server cannot call the webhook, it fails the request:

- A read or a write of an Agent, a Workspace or a TestKey fails if the request version is not the stored version of the object. The error names the conversion webhook. No object changes.
- A request for a different kind, for example a Hold, does not use the webhook and continues to work.
- The delete of a namespace can wait until the webhook is ready, because the API server lists each resource of the namespace.

## Downgrade

A downgrade to a release before v0.7.0 is not a `helm rollback`. Helm keeps the CRDs. They keep `v1beta1` as the storage version, and 3 of them name the conversion webhook. The API server refuses the removal of `v1beta1` from a CRD while `status.storedVersions` has it. This release has no downgrade procedure. Do the upgrade in a test cluster first.

## The removal of v1alpha1

| Release | `v1alpha1` |
|---|---|
| v0.7.0 | Served and deprecated. |
| v0.8.0 | Served and deprecated. |
| v0.9.0 | Not served. The upgrade needs step 3. |
| The release after v0.9.0 | Removed. |
