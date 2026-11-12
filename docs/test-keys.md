# Test keys

A TestKey is one short-lived gateway key for one Nookku test in a Workspace. The controller makes the key, puts it in the Workspace pod and revokes it.

## What is active

- The TestKey API, the Workspace field `spec.testKeys`, the TestKey controller and the key slot in the Workspace pod are in `main`.
- The controller issues keys only if the manager flag `--test-key-single-instance-gateway` is set. The chart sets it. Set it only if the gateway has 1 replica and 1 worker. Without it, a TestKey gets no key.
- The Nookku install, the plugin pin and the runtime dependencies are not active yet. They wait for owner approval. Koodam gives the key and the test folder, and you install Nookku yourself.

## Separate allowance

A TestKey has its own budget and TTL. It does not use the Workspace key or the Workspace budget. No total limit exists for the test keys of one Workspace. Only 1 TestKey of a Workspace can be active at a time.

All the processes in the Workspace share one trust boundary. Thus a test key is a separate count of spend, not a security boundary between processes.

## The Workspace opt-in

A Workspace without `spec.testKeys` permits no TestKey. If you set `spec.testKeys`, you must set `maxBudget` and `maxTTL`. Each must be more than zero. The Workspace also needs `spec.gateway`.

## The TestKey spec

All the spec fields are necessary. The API server refuses a change to the spec after the create.

| Field | Rule |
|---|---|
| `workspaceRef.name` | A DNS subdomain name. The Workspace is in the namespace of the TestKey. |
| `workspaceRef.uid` | Not empty. It must equal the Workspace UID, so a new Workspace with the same name gets no key. |
| `testID` | A letter or a digit, then letters, digits, `_` or `-`. |
| `budget` | A quantity more than zero. It must not exceed `maxBudget`. |
| `ttl` | A duration more than zero. It must not exceed `maxTTL`. |

[config/samples/core_v1alpha1_testkey.yaml](../config/samples/core_v1alpha1_testkey.yaml) is an example.

The controller refuses a key, and writes the reason to the condition `Active`, in these cases: the Workspace does not exist, the UID differs, the Workspace is suspended or in delete, no Profile binds the namespace, the Workspace has no gateway or no `testKeys`, or the budget or TTL exceeds the limit.

## The status

The status never holds the key value.

- `keyAlias`: the gateway key alias.
- `keyHash`: the SHA-256 hash of the key, 64 hexadecimal characters. The gateway refuses the hash for model calls.
- `expiresAt`: the time when the key stops. It is the issue time plus `ttl`.
- `revokedAt`: the time of the revoke.
- `usage`: the gateway spend, calls, failed calls and tokens. The controller reads it after the revoke.
- `conditions`: the condition `Active`. A message holds no key value and no upstream error text.

## The alias

The alias is `tk-<testID>-<uid>`. The controller cuts `testID` to 48 characters, and uses the first 8 characters of the TestKey UID. The gateway metadata has `koodam_test_id` and `koodam_testkey`.

## The key slot

The controller makes 1 Secret for each Workspace as the slot. The Workspace pod mounts it read-only at `/etc/koodam/test-gateway`. The mount is optional, so the pod starts with no key. The kubelet adds and removes the files when the slot changes. The files are:

| File | Content |
|---|---|
| `key` | The gateway key value. |
| `alias` | The key alias. |
| `base-url` | The gateway URL. |
| `model` | The model of the Workspace gateway. |

If a second TestKey finds the slot busy, it waits and shows the reason `SlotBusy`. A change to a slot file, or a missing slot, starts a revoke.

## Revoke first

The controller revokes in this order. Each step can run again.

1. Write the condition `Active=False` with the reason `Revoking`.
2. Block the key at the gateway.
3. Write the reason `Blocked`, and wait 75 s by default (`--test-key-revoke-wait`). This time is the gateway auth cache TTL plus a margin.
4. Read the spend. If the gateway shows a key that is not blocked, block it again and wait again.
5. Delete the slot Secret, write `revokedAt` and the `usage`, then remove the finalizer.

The revoke starts at `expiresAt`, at a TestKey delete, at a Workspace suspend or delete, and when no Profile binds the namespace. The finalizer `core.koodam.dev/testkey-revoke` keeps the TestKey until the revoke is done. A Workspace delete waits for the revokes of its TestKeys.
