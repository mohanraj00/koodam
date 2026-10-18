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

## One run, one command

```sh
koodam run -f agent.yaml --wait
```

This creates the Agent, shows the pod log, waits for the revoke and shows the spend. The other commands:

```sh
koodam get [NAME]        # phase, model, budget, spend
koodam spend [NAME]      # budget, spend, calls and tokens
koodam logs NAME         # the pod log of the run
koodam delete NAME       # the controller revokes the key first
```

Build it with `go build -o bin/koodam ./cmd/koodam`. It uses your kubeconfig, and `-n`, `--context` and `--kubeconfig` work as in kubectl.

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
