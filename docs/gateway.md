# The gateway model

Koodam will support 2 ways to run an agent:

- **Gateway.** The harness calls the models through a LiteLLM gateway. Koodam makes a gateway key for each run. Koodam supports this model today.
- **Subscription login.** The harness uses the login of a person, for example a Claude or a ChatGPT plan. This is the easy start, because it needs no gateway. The login comes with the profiles in v0.2.

This guide tells why to use the gateway and how to configure it.

## Why use the gateway

The gateway key of a run gives you 3 things that a subscription login does not give:

- **A budget.** The key permits one model and stops at the `budget` of the Agent. After the budget, the gateway refuses the calls of the run.
- **A revoke.** The key expires after `deadlineSeconds` plus a grace period. When the Job ends, or when you delete the Agent, the controller blocks the key before it deletes anything. A finalizer keeps the Agent until the block is done.
- **The real spend.** After the revoke, the controller reads the spend, the calls and the tokens of the key from the gateway. It writes them to the Agent status. `koodam spend` and `kubectl get agents` show them.

The key value is never in the status, in an event or in a log. The status has the key alias and the SHA-256 hash of the key. The gateway refuses the hash for model calls, so the hash is not a credential.

If a run ends with no gateway spend in the status, `koodam get`, `koodam spend` and `koodam run --wait` show a note with a link to this guide.

## Install the chart with a gateway

The chart has 2 gateway modes. [charts/koodam/README.md](../charts/koodam/README.md) has all the steps and values.

- `bundled`: the chart also installs LiteLLM and PostgreSQL. Use `values-bundled.yaml`.
- `existing`: the controller uses a LiteLLM gateway that you install. Set `gateway.url`. LiteLLM must have a database, because the controller makes keys and reads the spend.

For the 2 modes, make the Secret with the LiteLLM master key first. Read the key from a file, so that the key is not in your shell history:

```sh
kubectl create namespace koodam-system
kubectl -n koodam-system create secret generic koodam-gateway --from-file=master-key=./master-key
```

Then install the chart:

```sh
VERSION=0.1.0
helm install koodam oci://ghcr.io/mohanraj00/charts/koodam --version "$VERSION" -n koodam-system \
  -f "https://raw.githubusercontent.com/mohanraj00/koodam/v$VERSION/charts/koodam/values-bundled.yaml"
```

For an existing gateway, do not use `values-bundled.yaml`. Use `--set gateway.url=<address>`. If the agent pods use a different address to get to the gateway, set `gateway.agentUrl` also.

## Configure the models

The `model` of an Agent is a model name in the LiteLLM config. The key permits only this model.

- With the bundled gateway, add your models to `gateway.bundled.litellm.modelList`. This value is the LiteLLM `model_list`.
- With an existing gateway, add the model to the LiteLLM config of that gateway.

If the model name is not in the LiteLLM config, the gateway refuses the calls.

## Write the Agent

Set the model, the budget and the deadline in the Agent:

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
  deadlineSeconds: 900
  harness:
    name: claude-code   # or codex
```

The `budget` is in the currency of the gateway price table. The default `deadlineSeconds` is 900. The chart value `keyGrace` sets the grace period for the key TTL.

## What the pod gets

The controller puts the key and the gateway address in a Secret for the run. The pod gets them as env:

- **Claude Code:** `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_MODEL`. The other Claude Code model variables also get the Agent `model`, so that each call uses the model that the key permits.
- **Codex:** `OPENAI_API_KEY`, and a Codex config with a model provider for the gateway. Codex does not read `OPENAI_BASE_URL`, so the config sets the address.

You do not set these variables in the Agent `env`.

## Read the spend

```sh
koodam run -f agent.yaml --wait   # at the end: the phase and the spend
koodam spend fix-tests            # budget, spend, calls and tokens
kubectl get agents                # the spend column
```

The gateway key has the harness and the `user` of the Agent in its metadata. Thus you can group the spend in LiteLLM by harness or by user.
