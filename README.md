<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/koodam-mark-dark.svg">
    <img src="assets/brand/koodam-mark.svg" alt="Koodam" width="128">
  </picture>
</p>

# Koodam

Koodam (short for தொழிற்கூடம், "workspace") is where your agents live on Kubernetes.

I run coding agents every day. On a laptop, each run needs a model key, a login and skills by hand. The key stays valid after the run, and nothing records the spend. Koodam makes agent work a set of Kubernetes resources:

- **Agent.** One run as a Job. It gets a model key with a budget and a TTL, and a Role with only the access it asks for. At the end, Koodam blocks the key and writes the spend to the status.
- **Workspace.** A pod with a home on a PVC. You connect over SSH and run the harnesses in tmux. The session stays when the laptop closes.
- **Profile.** One login, one set of global files and skills for each Workspace in your namespaces.
- **App.** An application with versions, a traffic split, budgets and tenants.

Koodam is for developers and platform teams that already run Kubernetes. A platform team controls the resources with RBAC, NetworkPolicy and GitOps. [CONSTITUTION.md](CONSTITUTION.md) has the purpose and the rules.

The current release is v0.6.0. The API is `v1alpha1` and can change between tags. Status: pre-alpha.

## Quickstart

This runs one Agent on a local kind cluster, from a clean checkout. The bundled gateway answers the mock model `mock-1`, so you need no model key. You need `git`, `go`, `docker`, `kind`, `kubectl`, `helm` and `openssl`.

1. Get the code, make a cluster and build the CLI:

   ```sh
   git clone https://github.com/mohanraj00/koodam.git && cd koodam
   kind create cluster --name koodam
   go build -o bin/koodam ./cmd/koodam
   ```

2. Make the gateway master key. The key goes to `kubectl` on stdin, not to your shell history:

   ```sh
   kubectl create namespace koodam-system
   printf 'sk-%s' "$(openssl rand -hex 32)" | kubectl -n koodam-system \
     create secret generic koodam-gateway --from-file=master-key=/dev/stdin
   ```

3. Install the release chart with the bundled gateway, and wait for it:

   ```sh
   VERSION=0.6.0
   helm install koodam oci://ghcr.io/mohanraj00/charts/koodam --version "$VERSION" \
     -n koodam-system -f charts/koodam/values-bundled.yaml --wait --timeout 10m
   ```

4. Run one Agent. It calls `mock-1` one time through the gateway:

   ```sh
   kubectl create namespace dev-sample
   cat > agent.yaml <<'EOF'
   apiVersion: core.koodam.dev/v1alpha1
   kind: Agent
   metadata:
     name: hello
     namespace: dev-sample
   spec:
     image: docker.io/curlimages/curl:8.11.1
     command:
     - sh
     - -c
     - >-
       curl -s -o /dev/null -w '%{http_code}\n'
       -H "Authorization: Bearer $OPENAI_API_KEY"
       -H 'Content-Type: application/json'
       "$OPENAI_BASE_URL/chat/completions"
       -d '{"model":"mock-1","messages":[{"role":"user","content":"hi"}]}'
     model: mock-1
     budget: "2"
   EOF
   bin/koodam run -f agent.yaml --wait
   ```

   The pod log shows `200`. Then the command waits for the revoke and shows the spend.

5. Read the result, then delete the cluster:

   ```sh
   bin/koodam get hello -n dev-sample
   kind delete cluster --name koodam
   ```

## Next docs

| Page | Operation |
|---|---|
| [Install a release](docs/quickstart.md) | The chart and the CLI on a real cluster |
| [Agents](docs/agent-runs.md) | `Agent`: one run with a key, a Role and a budget |
| [Workspaces](docs/workspaces.md) | `Workspace`: a pod with a PVC home over SSH |
| [Profiles](docs/profiles.md) | `Profile`: the namespace binding, the login and the global files |
| [Test keys](docs/test-keys.md) | `TestKey`: a short-lived key for one Nookku test |
| [Apps](docs/apps.md) | `App` and `AppVersion`: versions, weights, the gate and key rotation |
| [Holds](docs/holds.md) | `Hold`: stop or quarantine a workload |
| [Spend budgets](docs/spend-budgets.md) | `SpendBudget`: a spend limit for each day or month |
| [Tenants](docs/tenants.md) | `TenantQuota`: a quota and a rate limit for each tenant of an App |
| [The CLI](docs/cli.md) | Each `koodam` command |
| [Spend API](docs/spend-api.md) | The runs, the spend and the budgets over HTTP |
| [Operations page](docs/operations-page.md) | The read-only page of the spend API |

Background: [architecture](docs/architecture.md), [gateway](docs/gateway.md), [traced egress](docs/traced-egress.md), [threat model](docs/threat-model.md) and [metrics](docs/metrics.md). [CHANGELOG.md](CHANGELOG.md) has the changes and the known limits of each release. [SECURITY.md](SECURITY.md) tells how to report a problem.

## Plan

| Version | Adds |
|---|---|
| v0.1 | `Agent` runs as a Job, key and Role for each run, spend in the status, `koodam` CLI, Helm chart |
| v0.2 | profiles and workspaces: log in once, and every harness in your namespaces shares the login, skills, config and memory |
| v0.3 | traced egress: every call out of an agent goes through a proxy and gets a span. In the spike, 360 of 360 planted calls got a span with the correct run, and 0 of 23 bypass attempts got through |
| v0.4 | apps and Nookku: an `App` resource with versions and a traffic split, traced ingress, and a rollout that waits for the Nookku tests to pass |
| v0.5 | controls for an app in production: a budget for each period, metrics, key rotation, a kill switch and a quarantine |
| v0.6 | agentic apps at scale: workload identity, MCP tools, approvals, durable runs, autoscaling, and a test on a public cloud |
| v0.7 | the beta API (`v1beta1`) |

[CONSTITUTION.md](CONSTITUTION.md#evidence) has the evidence rules for each claim here.

## Build and contribute

```sh
make check   # the Go tests and the Python tests of tools/
make run     # the controller, against the cluster of your kubeconfig
```

[docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) has the layout. Open an issue first. [CONTRIBUTING.md](CONTRIBUTING.md) has the rules. The license is Apache-2.0. [docs/brand.md](docs/brand.md) has the rules for the mark in [`assets/brand/`](assets/brand/).

[Bring your Claude Code to Kubernetes, not a new agent platform](https://mohanraj00.github.io/tech/introducing-koodam/) is the blog post: why I built Koodam, and a day with a Workspace.
