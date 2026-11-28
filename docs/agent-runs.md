# Agents

Release: v0.6.0.

An `Agent` is one agent run as a Kubernetes Job. The controller gives the run its credentials, and takes them back when the run ends.

## Example

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: Agent
metadata:
  name: fix-tests
  namespace: dev-sample
spec:
  image: ghcr.io/example/coding-agent:latest
  command: ["sh", "/opt/tasks/run.sh"]
  model: local-agent
  budget: "2"
  rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
```

```sh
koodam run -f agent.yaml --wait
```

The [README quickstart](../README.md#quickstart) has a full example with the mock model.

## The spec

| Field | Description |
|---|---|
| `image`, `command` | Required. The agent container. |
| `budget` | Required. The maximum spend of the run, in the currency of the gateway price table. |
| `model` or `models` | Set one. The gateway models that the key permits. The first model is the model of the harness. |
| `deadlineSeconds` | The time limit of the Job. The default is 900. The key TTL is this value plus the chart value `keyGrace`. |
| `rules` | The RBAC rules of the run Role, in the namespace of the Agent only. |
| `env` | Env variables of the agent container. |
| `user` | The owner of the run. It goes into the key metadata. |
| `harness` | A harness to install in the pod. See [Harnesses](#harnesses). |
| `podTemplate` | Changes to the run pod. [workspaces.md](workspaces.md#the-pod-template) has the rules. |
| `output.path` | One output file. The status gets its SHA-256 digest. [agent-output.md](agent-output.md) has the rules. |
| `approval.waitSeconds` | The run waits for a signed approval before its Job starts. See [Approval](#approval). |

## Result

1. The controller makes a gateway key for the model, with the budget and a TTL. The key goes into a Secret of the run.
2. It makes a ServiceAccount, a Role and a RoleBinding with `rules` and nothing more.
3. It makes a Job. The pod gets the key and the token. Nothing else is mounted.
4. When the Job ends, the controller blocks the key first. Then it deletes the credentials and writes the spend to the status.

`kubectl get agents` shows the columns `PHASE`, `MODEL`, `HARNESS`, `BUDGET`, `SPEND` and `AGE`. `koodam spend NAME` shows the calls and the tokens too.

The phase is `Pending`, `Running`, `Succeeded` or `Failed`. `status.runID` is the run identity. The agent container has it in `KOODAM_RUN_ID`.

The controller deletes a finished Job and its pod 24 h after the end (the chart value `jobTTLSeconds`). Then `koodam logs` has no log. The Agent keeps the status and the spend.

`koodam delete NAME` deletes an Agent. A finalizer keeps the Agent until the controller blocks the key.

## Harnesses

To run Claude Code or Codex, add a harness. The image does not need to contain it:

```yaml
spec:
  harness:
    name: codex        # or claude-code
    version: 0.163.0   # optional. With no version, the pod gets the latest version
    libc: glibc        # optional, glibc or musl. Claude Code only
```

- An init container downloads the binary from the vendor and checks its sha256. The binary is in `/koodam/bin`, first on the `PATH`.
- If `env` has a `PATH`, Koodam does not change it. Put `/koodam/bin` in it.
- The pod needs egress to `downloads.claude.ai` (Claude Code), or to `github.com`, `release-assets.githubusercontent.com` and `rekor.sigstore.dev` (Codex).
- The key metadata has the harness, so you can group the gateway spend by harness.
- [gateway.md](gateway.md#what-the-pod-gets) lists the env that each harness gets.

Put the instructions and the skills in the repo of the run, so both harnesses read the same files:

- **Instructions:** write `AGENTS.md`. If the repo has a `CLAUDE.md`, put `@AGENTS.md` in it.
- **Skills:** put them in `.agents/skills/<name>/SKILL.md`. Link them for Claude Code: `mkdir -p .claude && ln -s ../.agents/skills .claude/skills`.

## Approval

With `spec.approval`, the run has no pod and no key until a signed approval record permits it:

```yaml
spec:
  approval:
    waitSeconds: 3600   # default 86400. You cannot change it after create.
```

The Profile of the namespace sets the key set, the purpose and the maximum age of a record in its `spec.approval`, so the Agent owner cannot select them. An `ApprovalRecord` in the namespace of the Agent carries the record. `status.approval` has the request document and its subject. The condition `Approval` gives the state. [evidence-gate.md](evidence-gate.md) has the record rules.

## Errors

Read the conditions with `kubectl describe agent NAME`.

| Condition and reason | Meaning |
|---|---|
| `Provisioned=False`, `Invalid` | The spec failed a check of the controller, for example a rule that is not in the `agent-ceiling` ClusterRole, or a name of more than 60 characters. The phase is `Failed`. |
| `Provisioned=False`, `InvalidPodTemplate` | The pod template has a field that Koodam owns, or a Secret of Koodam. |
| `Provisioned=False`, `CostGroupNotAllowed` | The Profile does not permit the cost group label of the Agent. |
| `Provisioned=False`, `PeriodBudgetExceeded` | The `SpendBudget` of the namespace has no room. The run stays `Pending`. |
| `Revoked`, `JobComplete`, `JobFailed`, `Stopped` or `AgentDeleted` | Why the run ended and the revoke started. |
| `Approval=False`, `Denied`, `Expired`, `NoKeySet` or `ApprovalTimeout` | The run stops: a reject, an old record, no key set in the Profile, or no accept in `waitSeconds`. |
| `Approval=Unknown`, `Waiting`, `NotYetValid`, `SubjectMismatch` or `NotVerified` | The run stays paused. |

The API server refuses an image or a command in the pod template, and a change of `spec.approval` after create. `koodam run` stops with `agent/NAME exists in namespace NS` if the name is in use.
