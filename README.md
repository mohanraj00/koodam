# Koodam

Koodam (கூடம், "hall") is where your agents live on Kubernetes. One `Agent` resource gets a model key with a budget and a TTL, a ServiceAccount with only the access it asks for, and a Job. When the run ends, Koodam revokes all of it and writes the spend to the status.

I run coding harnesses (Claude Code, Codex) and my own agents on a dev cluster. Each one needs a key, a Role, a login, my skills and my config. Today I wire that by hand for every run, and the credentials outlive the run. Koodam does the wiring and the cleanup.

> Status: pre-alpha. The API will change.

## What one Agent gets (v0.1 target)

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: Agent
metadata:
  name: fix-tests
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

- A gateway key (LiteLLM today) that permits one model, stops at the budget and expires after the deadline.
- A ServiceAccount, a Role and a RoleBinding in the Agent's namespace, with the rules above and nothing more.
- A Job that gets the key and the token. Nothing else is mounted.
- After the run: the key is blocked, the credentials are deleted, and `kubectl get agents` shows the spend.

To run Claude Code or Codex, add a harness. The image does not need to contain it:

```yaml
spec:
  harness:
    name: codex        # or claude-code
    version: 0.163.0   # optional. With no version, the pod gets the latest version
    libc: glibc        # optional, glibc or musl. Claude Code only
```

An init container downloads the binary from the vendor when the pod starts and checks its sha256. The binary is in `/koodam/bin`, first on the `PATH`. If the Agent `env` has a `PATH`, Koodam does not change it, so put `/koodam/bin` in it. Koodam does not keep or redistribute the binary. The pod needs egress to `downloads.claude.ai` (Claude Code) or to `api.github.com` and `github.com` (Codex).

Put the instructions and the skills in the repo that the run works on. Then both harnesses read the same files:

- **Instructions:** write `AGENTS.md`. Codex reads it. Claude Code reads it if the repo has no `CLAUDE.md`. If the repo has a `CLAUDE.md`, put `@AGENTS.md` in it.
- **Skills:** put them in `.agents/skills/<name>/SKILL.md`. Codex reads this folder. Claude Code reads `.claude/skills`, so link it: `mkdir -p .claude && ln -s ../.agents/skills .claude/skills`.

Skills and config for each user, outside the repo, come with the profiles in v0.3.

A run has one harness or none. If a run has a harness, `kubectl get agents` and `koodam get` show it, and the gateway key has it in the metadata (`harness`). Thus you can group the gateway spend by harness.

## One run, one command

```sh
koodam run -f agent.yaml --wait
```

This creates the Agent, shows the pod log, waits for the revoke and shows the spend. The other commands:

```sh
koodam get [NAME]        # phase, model, budget, spend, start and end time
koodam spend [NAME]      # budget, spend, calls and tokens
koodam logs NAME         # the pod log of the run
koodam delete NAME       # the controller revokes the key first
```

Build it with `go build -o bin/koodam ./cmd/koodam`. It uses your kubeconfig, and `-n`, `--context` and `--kubeconfig` work as in kubectl.

For `koodam logs` and `run --wait`, the CLI user needs `get` on jobs, `list` on pods and `get` on pods/log in the namespace. The Agent viewer and editor ClusterRoles do not give them.

The controller deletes a finished Job and its pod 24 h after the end (the chart value `jobTTLSeconds`). After that, `koodam logs` has no log. The Agent keeps the status and the spend. The controller records the result before it deletes the Job, so a controller restart does not lose the result. If the controller is down, finished Jobs stay until it starts again.

## Install

The Helm chart installs the controller and the `Agent` CRD. The controller image is `ghcr.io/mohanraj00/koodam`. The chart uses an existing LiteLLM gateway, or it installs its own:

```sh
kubectl create namespace koodam-system
kubectl -n koodam-system create secret generic koodam-gateway --from-file=master-key=./master-key
helm install koodam charts/koodam -n koodam-system -f charts/koodam/values-bundled.yaml
```

With an existing gateway, pass `--set gateway.url=<address>`. It has no default, and the install stops without it. [charts/koodam/README.md](charts/koodam/README.md) has the steps.

## Plan

| Version | Adds |
|---|---|
| v0.1 | `Agent` runs as a Job, key and Role for each run, spend in the status, `koodam` CLI, Helm chart |
| v0.2 | traced ingress and egress: every call in and out of an agent goes through a proxy and gets a span |
| v0.3 | profiles: log in once, and every harness in your namespaces shares the login, skills, config and memory |
| v0.4 | MCP tools with credentials for each run |
| later | long-running agents, managed clusters |

Each version starts with a spike that decides go or no-go before the build. The spike records are the evidence for every claim here.

## Build

```sh
make manifests generate
make test
make run    # against the cluster in your current kubeconfig
```

Koodam is built with [Kubebuilder](https://book.kubebuilder.io). [docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) is the generated guide for the layout.

## License

Apache-2.0.
