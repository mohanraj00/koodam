# Threat model

This document gives the threats that Koodam controls. For each threat, it gives the control, the test that proves the control, and the risk that stays. [SECURITY.md](../SECURITY.md) tells you how to report a problem.

| Item | Value |
| --- | --- |
| Owner | The maintainer |
| Last review | 2026-11-27, for v0.5.0 |
| Next review | Before each release tag, as step 6 of the [release checklist](release-checklist.md) |

A test name in this document is a Go test function, or a Ginkgo `It` text in quotes. A test row that names an open PR gives a control that is not on `main` yet. When that PR merges, the next review changes the row to the test file on `main`.

## Assets and trust boundaries

The assets are:

- the gateway master key, which the controller holds;
- the gateway key of each Agent run, Workspace, TestKey and AppVersion;
- the login files, the SSH host key and the home PVC of a Workspace;
- the spend data of the runs;
- the identity of the caller of an App.

The trust boundaries are:

1. **The cluster operator and the controller.** The operator installs the chart and owns the `koodam-system` namespace. The controller and the operator are trusted.
2. **A namespace user.** A user who can make an Agent or a Workspace in a namespace is trusted in that namespace only. The user is not trusted with the master key or with a different namespace.
3. **The code in a pod.** The code in an Agent run pod, a Workspace pod or an App pod is not trusted. It can be a harness that follows a bad prompt.
4. **The gateway and the internet.** LiteLLM is a dependency that the controller trusts with the master key. The internet is not trusted.

## T1. The master key leaks

The master key can make, renew and block each key, and read each spend. A leak gives full control of the gateway.

- **Control.** The controller reads the master key from a file (`--gateway-master-key-file`). The chart mounts the file from the Secret of `gateway.masterKeySecret` into the controller pod. In the bundled mode, the LiteLLM pod also reads it. An Agent, Workspace or App pod gets its own key, and never the master key. The gateway client removes the key values and the master key from each error. It sends each key in a body or a header, not in a URL, and it does not follow a redirect.
- **Test.** `internal/gateway`: `TestErrorsDoNotContainTheMasterKey`, `TestErrorsDoNotContainTheKey`, `TestKeyIsNotInURLs` and `TestRedirectsAreNotFollowed`.
- **Residual risk.**
  - A person who can read Secrets in `koodam-system`, or exec into the controller pod, gets the master key. Give that access to the operator only.
  - No test checks that the chart gives the master key only to the controller pod and the bundled LiteLLM pod. The chart templates are the record.
  - LiteLLM stores the master key and the keys. A problem in LiteLLM is outside this model.

## T2. A key stays valid after its use ends

A key that stays valid after the run lets a copy of the key spend more.

- **Control.** Each key permits only the models of its object, with a budget and a TTL. When the Job ends, or when the user deletes the object, the controller blocks the key first. Then it deletes the access and reads the spend. A finalizer holds the object until the gateway confirms the block. The controller blocks a key by the hash in the status when the Secret is gone.
- **Test.** `internal/controller/agent_controller_test.go`: "revokes when the Job ends", "revokes when the Agent is deleted during a run", "revokes after a controller restart during a run" and "revokes by the status hash when the Secret is gone". `internal/controller/workspace_controller_test.go`: "blocks the key before it stops the pod when the binding goes, and runs again when it returns". `internal/controller/appversion_controller_test.go`: "keeps the Secret, the objects and the finalizer while the gateway does not confirm the block". `internal/gateway`: `TestCreateKeySendsBudgetModelAndTTL`.
- **Residual risk.** The key is valid until the block completes. A copy of the key can spend up to the budget in that time.

## T3. A credential goes into a log, an event or the status

- **Control.** The controller writes the key alias and the SHA-256 hash of the key, and no key value. The SSH host key and the login files stay in Secrets.
- **Test.** `internal/controller/workspace_controller_test.go`: "writes no private key text to the status, the events or the log". `internal/controller/appversion_controller_test.go`: "makes the key, the Role, the Deployment and the Service, and writes only the alias and the hash".
- **Residual risk.** A harness in a pod can print its own key to its own log. Koodam does not control the output of the code in a pod.

## T4. The code in a Workspace pod gets more access than the pod

A Workspace pod runs code that the user and the harnesses start. The pod is the trust boundary of that code.

- **Control.** The pod meets the Pod Security `restricted` profile. It runs as uid 1000 with no privilege escalation, all capabilities dropped, the `RuntimeDefault` seccomp profile and no hostPath volume. It has no ServiceAccount token, so it has no Kubernetes API access. It has no Service and no public port. `koodam tunnel` connects through the Kubernetes API, with the RBAC of the developer for `pods/portforward`. sshd listens on port 2222 and accepts only the public keys of `spec.sshPublicKeys`. With `workspace.networkPolicy`, the chart permits ingress to port 2222 from each source.
- **Test.** `internal/controller/workspace_controller_test.go`: "meets the Pod Security restricted profile". `test/e2e/networkpolicy_test.go`: "lets a Workspace pod reach only DNS, the gateway and the public internet".
- **Residual risk.**
  - The pod holds the gateway key, the login Secret and the home PVC. Code in the pod can read them. Treat each harness in a Workspace as able to use them.
  - A person who has `pods/exec` or `pods/portforward` in the namespace can reach the pod.
  - A caller that can reach the pod IP can connect to port 2222 directly, with no `pods/portforward` permission. The SSH key is then the only control. A removal of the RBAC permission does not stop this caller. Remove the key from `spec.sshPublicKeys` to stop it.
  - The pod shares the node kernel. Set `runtimeClassName: gvisor` in `spec.podTemplate` for a stronger boundary. See [workspaces.md](workspaces.md).

## T5. A pod template reads a Secret of Koodam

The controller makes the pod, so the pod can read a Secret that the user cannot read.

- **Control.** The controller refuses a reference to a Secret with the prefix `kd-`, and to a Secret that a Koodam object owns. It refuses the template before it makes a key.
- **Test.** `internal/controller/workspace_controller_test.go`: "refuses a reference to a Secret that Koodam owns, and accepts a user Secret". `internal/controller/agent_controller_test.go`: "fails an Agent with a spec.podTemplate that the merge refuses, and makes no key". `internal/controller/appversion_controller_test.go`: "makes no key for a template with a Secret name of a Workspace that does not exist". `internal/podtemplate`: `TestMergeRefuses`.
- **Residual risk.** A reference to a Secret that does not exist is permitted. [workspaces.md](workspaces.md) gives this limit.

## T6. A run gets more Kubernetes access than the operator permits

- **Control.** The `agent-ceiling` ClusterRole is the most that the Role of a run or a version can grant. The controller refuses a rule outside it before it makes a key.
- **Test.** `internal/controller/agent_controller_test.go`: "fails a rule outside the ceiling before it makes a key". `internal/controller/appversion_controller_test.go`: "makes no key for a rule that is not in the ceiling".
- **Residual risk.** The operator sets the ceiling. A wide ceiling gives each run that access.

## T7. A pod sends data out with no trace

With traced egress, the egress proxy records each call of a pod. A pod that goes around the proxy has no record.

- **Control.** A NetworkPolicy makes the pod egress default-deny, except to the proxy, the API server and `extraEgress`. The pod DNS is the proxy. The proxy refuses a source pod that it does not know. It refuses a connection to a private, loopback, link-local or metadata address. It refuses a CONNECT host that is not equal to the TLS SNI, and a call with no TLS on the TLS port. A header from an App pod cannot change the pod identity. The span of a model call does not have the key.
- **Test.** `test/e2e/egress_test.go`: "blocks each call that does not go through the proxy" and "traces the planted calls of an Agent and a Workspace with the pod identity". `internal/egress`: `TestProxyRefusesUnknownSource`, `TestIsPublic`, `TestConnectRefusesFront`, `TestConnectRefusesNoTLS`, `TestAppHeadersCannotChangeIdentity` and `TestLLMRouteKeepsTheKeyOutOfTheSpan`. `internal/controller/appversion_controller_test.go`: "makes a default-deny NetworkPolicy with egress to the proxy only, and then the egress is traced".
- **Residual risk.** The limits in [traced-egress.md](traced-egress.md) apply:
  - The proxy does not decrypt TLS. It does not stop a tunnel or DNS over HTTPS inside an allowed call.
  - A managed cluster can have more paths out, for example the cloud metadata service. The e2e test checks kind only.
  - A call to the API server or to an `extraEgress` peer has no span.

## T8. A caller of an App forges an identity

- **Control.** With the claim check, each request to an App goes through the `koodam-claim` container. It accepts RS256 and ES256 only, and checks the signature, `iss`, `aud`, `exp` and `nbf`. A request with no valid claim gets 401. The check sets `Koodam-Caller` and `Koodam-Tenant`, and removes the values that the caller sends. The JWKS must have public keys only. The NetworkPolicy of the version permits ingress to the check port only.
- **Test.** `internal/appclaim`: `TestVerifyRefuses`, `TestCheckHandler` and `TestParseKeySetRefuses`.
- **Residual risk.** The kubelet sends the probes directly to the `app` container. The check protects a version only while its NetworkPolicy exists. The `ClaimCheck` condition shows this state. A JWKS rotation needs a change of the chart value.

## T9. A viewer reads the spend of a different namespace

The spend API and the operations page show the runs and the spend.

- **Control.** Each request needs a bearer token. In the viewer mode, the server checks the token with a TokenReview. Then it does a SubjectAccessReview for `list` on `agents.core.koodam.dev` in each namespace, and returns only the permitted namespaces. No response has a key value or a key hash. The page keeps the token in memory only, and its Content-Security-Policy permits no inline script and no other origin.
- **Test.** Each row is a test on `main`:
  - `internal/spendapi/api_test.go`, `TestServeRunsAndSpend`. Each call of this test checks that no body has a key value, a key hash or the token.
  - `internal/spendapi/viewer_test.go`, `TestViewerSeesOnlyPermittedNamespaces` and `TestViewerRefusals`.
  - `internal/spendapi/run_test.go`, `TestViewerReadsOneRun` and `TestRunDetail`. The run path checks the namespace of the path only, and a container state has no termination message.
  - `internal/spendapi/redact_test.go`, `TestEachRedactionPattern`, has a test for each redaction pattern. `internal/spendapi/run_test.go`, `TestConditionMessagesAreRedacted`.
  - `internal/opspage/page_test.go`, `TestPolicyAndPageHaveNoInlineCode` and `TestPageKeepsTheTokenInMemory`.
  - `examples/operations-page/main_test.go`, `TestExample`.
  - `internal/spendapi/budgets_test.go`, `TestServeBudgets`. `/v1/budgets` uses the same namespace scope as the other paths.
- **Residual risk.** The static token mode and the claim mode show each configured namespace to each valid caller. Use the viewer mode for people. In the viewer mode, the server needs `create` on `tokenreviews` and `subjectaccessreviews`. Give this access to the ServiceAccount of the spend API only.

## T10. A tenant or a zone goes past its limit

A tenant of an App can use more than its quota. A call or a pod can go out of its zone.

- **Control.** The claim check refuses a tenant over its rate limit or quota with a typed reason. The code in an App pod cannot select the tenant of a model call: the claim check signs a tenant token for the tenant of each request, and the egress proxy accepts a model call of an App pod only with a valid token. The proxy then sets the customer of the gateway call. A model call of an Agent pod or a Workspace pod gets a reserved customer. The proxy refuses the gateway key of an AppVersion in a model call of a pod that is not a pod of that AppVersion. The claim check refuses a caller of a different zone. The gateway refuses a model route of a different zone. The egress proxy refuses a host of a different zone. The admission policy refuses a pod placement out of the zone.
- **Test.** Each row is a test on `main`:
  - `internal/appclaim/tenant_test.go`, `TestCheckTenantRefusals`.
  - `internal/egress/tenant_test.go`, `TestTenantTokenBindsTheModelCall` and `TestTenantTokenRefusals`.
  - `internal/controller/appversion_token_test.go`, `TestTenantTokenKeyOfAVersion` and `TestTenantTokenTemplateNames`.
  - `internal/egress/tenantkeys_test.go`, `TestTenantKeysWatch`. `internal/egress/appkey_test.go`, `TestAppKeyOutOfPodIsRefused`. `test/e2e/app_test.go`: "counts a model call only on the tenant of the token".
  - `internal/appclaim/zone_test.go`, `TestCheckRefusesZone`.
  - `test/chart/zone_test.go`, `TestZoneRoutes`.
  - `internal/egress/zone_test.go`, `TestOutOfZoneRefused`.
  - `internal/controller/zone_policy_test.go`, "refuses a node selector for a different zone or region".
- **Residual risk.** The tenant label comes from the claim, so the issuer must be trusted. An app can use the tenant token of a request for a different model call until the token ends. A principal that can create pods or read Secrets in the namespace of an App can read the seed of the tenant token and sign a token for each tenant label. The proxy trusts the pod labels for the identity of a pod, so a principal that can change pod labels can change that identity. A gateway of the mode `existing` at a public address, or an `extraEgress` rule that opens the gateway, lets a pod call the gateway around the proxy. The key check of the proxy is defense in depth, not a full bind of the gateway key to the pod: an App pod can send its key to a different pod, and that pod can use the key in a call around the proxy, or in a key location that a different gateway version reads. An app can also make the model call itself for a different pod, with a token of a tenant that sent a request in the life of the token. The claim check refuses a tenant at its quota from the gateway count of that tenant, so each of these paths can also fill the quota of a tenant that did not cause the calls. With `apps.claimCheck.tenantToken=false`, the code in an App pod selects the tenant label of each model call. A `TenantQuota` with no `appRef` gives a tenant its rate one time for each App of the namespace. Each pod of an App has its own token bucket with a burst of 1 or more, so a tenant can get one request from each pod at one time, which can be more than the burst of its `TenantQuota` ([#428](https://github.com/mohanraj00/koodam/issues/428)). The pods of an App do not read a new limits file at the same time. Thus, after a new version gets a weight or a version gets more replicas, a tenant can get more than its rate until each pod reads the new file ([#428](https://github.com/mohanraj00/koodam/issues/428)). The limit `requests` of a `TenantQuota` comes from a read of the gateway count at an interval. Thus a tenant can make more model calls than this limit until each pod has the quota mark, and only a rate limit or a spend limit of the same `TenantQuota` limits these calls ([#429](https://github.com/mohanraj00/koodam/issues/429)). The zone controls apply only to the namespaces of the install. The placement policy of a zone is a list of refused placements, not a full control of the node of a pod. The owner of an App can direct a pod to a node of a different zone with a field of the pod template that the policy does not examine, for example a pod affinity, a different node label, a toleration or a scheduler name. Thus a cluster with nodes of 2 zones also needs a control of the cluster, for example a node selector for each namespace ([#433](https://github.com/mohanraj00/koodam/issues/433)). [tenants.md](tenants.md) gives the typed reasons.

## T11. A namespace user raises the period budget of the namespace

A user of a namespace can try to change the `SpendBudget` of the namespace, so that more runs start in the period.

- **Control.** The chart gives no write role for `spendbudgets`, except the role of the manager. The viewer role has only `get`, `list` and `watch`, and no role of the chart has an aggregate label, so the Kubernetes roles `edit` and `admin` get no rule. The API accepts only the name `default`, so a user cannot add a second budget for the namespace.
- **Test.** `test/chart/spendbudget_test.go`, `TestSpendBudgetRolesOnlyRead`. `internal/controller/spendbudget_crd_test.go`: "refuses an object that is not valid". `internal/controller/spendbudget_ledger_test.go`: "holds a run until the next period when the period limit has no room" and "releases the reservation of a deleted run".
- **Residual risk.** A cluster admin can bind a write role to a namespace user. A role with a wildcard resource, outside the chart, also gives write access. A user who can delete Agents can delete a waiting run, but not the reservation of an active run: the finalizer moves the reservation to the spend. The ledger counts only Agent runs.

## T12. Untrusted content gives instructions to the model

A document, a web page or a tool reply that an agent reads can hold instructions for the model. A harness that follows them can try to send data out or to change the work of the run.

- **Control.** The egress policy is the boundary (T7): a pod that follows an instruction still cannot call a host that its policy refuses, and each call that goes out has a span. The optional [gateway guard](gateway-guard.md) is one more layer. It examines the tool replies and the documents in each model request with fixed rules, and it records, marks or blocks a match. Each decision gives a gateway log line with the policy version and a digest of the content, and no content.
- **Test.** `tests/test_guard_hook.py` (a planted injection and a clean reply in each request form, for each action), `test/chart/guard_test.go`, and the kind test in `test/e2e/egress_test.go`: the guard records a planted tool reply, and the egress proxy refuses the host that it names. `make guard-image-check` proves each action in the pinned gateway image, and that a request cannot turn off the hook.
- **Residual risk.** The guard is a tripwire for known forms, not a defense. [gateway-guard.md](gateway-guard.md) lists what it cannot detect, for example a paraphrase, encoded text or an instruction across blocks. The guard is off by default. It covers only the bundled gateway and the model calls with a gateway key. A model call with a subscription login goes in a TLS tunnel that no part of Koodam can read. With the action `block`, a hostile page or a false match stops the run, because the history sends the block again. An instruction can still misuse the hosts that the egress policy permits, the tools of the harness and the files of the pod.

## T13. A workload owner clears an incident control

The owner of a workload that an admin stopped or quarantined can try to clear the control, or a person can try to write a different user in the record of the control.

- **Control.** The control is a cluster-scoped `Hold`, not a field of the workload. The chart gives no write role for `holds`, and no role of the chart has an aggregate label, so the Kubernetes roles `edit` and `admin` get no rule. The CRD makes the target, the action, the reason and the user immutable, and the release immutable after it is set. The admission policy `koodam-hold` refuses a user that is not the user of the request, and the delete of a Hold with no release. For the action `Stop`, the controller blocks the gateway key of the target before it stops the pods, and the target does not start again until the release. For the action `Quarantine`, the controller blocks the gateway key and adds a finalizer to the workload, and the egress proxy refuses each call of the pods of the target.
- **Test.** `test/chart/hold_test.go`, `TestHoldRolesOnlyRead` and `TestHoldPolicy`. `internal/controller/hold_crd_test.go`: "refuses a user that is not the user of the request", "keeps the target, the action, the reason and the user immutable" and "refuses the delete of a Hold with no release". `internal/controller/hold_controller_test.go`: "blocks the key before it deletes the Job, and the run does not start again after the release", "blocks the key before it stops the pod, and starts again with a new key after the release" and "blocks the key before it deletes the objects, with no drain". `test/e2e/hold_test.go`: "stops an Agent run: it blocks the key and leaves no access". `internal/controller/hold_controller_test.go`: "quarantines a run, keeps its Job, and stops the run after the release", "quarantines the Workspace: blocks the key and keeps the pod, then stops and starts again" and "quarantines the versions of an App: blocks the key and keeps the pods, then revokes after the release". `internal/egress/quarantine_test.go`: `TestProxyRefusesAQuarantinedPod`, `TestConnectQuarantineEndsTheTunnel` and `TestQuarantinesProfile`.
- **Residual risk.** A kustomize install needs the folder `config/hold/admission` for the policy. Without the policy, an admin can write a different user. A cluster admin can delete the policy or bind a write role to a namespace user. The quarantine has open limits. A direct delete of the pods, or a foreground delete, can stop the quarantined processes, and a user who can patch the pods can change the labels that the proxy reads ([#452](https://github.com/mohanraj00/koodam/issues/452)). A Workspace deleted before the reconcile can lose its pod ([#454](https://github.com/mohanraj00/koodam/issues/454)). A Profile across 2 installs where only 1 has the egress proxy can show `Quarantined` while the other install still sends calls out ([#455](https://github.com/mohanraj00/koodam/issues/455)). The proxy sees only the calls that go through it.

## Out of scope

- The security of LiteLLM, PostgreSQL, Kubernetes and the model providers.
- A cluster operator who acts against the users.
- The harness binaries. The init container checks the sha256 of each download, but Koodam does not review the harness code.
