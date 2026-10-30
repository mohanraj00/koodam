# Test keys

Issue #192 adds a short-lived gateway key for one Nookku test in a Workspace.

**API only.** This version adds the `TestKey` type and the Workspace field `spec.testKeys`. No controller uses them. Koodam does not make a key, a Secret or a mount for a TestKey. No budget, TTL, expiry, revoke or one-key limit is applied.

**Separate allowance.** A TestKey has its own budget and TTL. It does not use the Workspace key or the Workspace budget. In v0.4, no total limit for the test keys of a Workspace exists.

**Shared trust boundary.** The later controller gives the test key to the Workspace pod in a read-only file. All the processes in the Workspace share one trust boundary. Thus a test key is a separate count of spend, not a security boundary between processes.

**Admitted fields.** All the spec fields are necessary. The API server refuses a change to the spec after the create. `workspaceRef.name` is a DNS subdomain name and `workspaceRef.uid` is not empty. `testID` has the Nookku rule: a letter or a digit, then letters, digits, `_` or `-`. `budget` is a quantity more than zero and `ttl` is a duration more than zero. The status never holds the key value. [config/samples/core_v1alpha1_testkey.yaml](../config/samples/core_v1alpha1_testkey.yaml) is an example.

**Opt-in limits.** A Workspace without `spec.testKeys` permits no TestKey. If you set `spec.testKeys`, you must set `maxBudget` and `maxTTL`, and each must be more than zero.

**No controller check yet.** The API server does not make sure that the Workspace UID is correct, that the budget and the TTL are in the Workspace limits, or that a Workspace has only one active TestKey. The later controller PR adds these checks, the key alias in the status, the expiry and the revoke.
