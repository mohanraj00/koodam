# Koodam Helm chart

This chart installs the Koodam controller and the Agent CRD. The controller uses a LiteLLM gateway that you install. The chart does not install LiteLLM or a database.

## What the chart installs

- The Agent CRD (from `crds/`).
- The controller Deployment and its ServiceAccount.
- The `manager-role` ClusterRole: the rules that the reconciler needs.
- The `agent-ceiling` ClusterRole: the most that a run Role can grant.
- The leader election Role, in the release namespace.
- If `metrics.enabled` is true: the metrics Service and the metrics-auth ClusterRole.

## Install with an existing gateway

1. Install LiteLLM with a database. The controller makes a key for each Agent and reads the spend. These operations need the LiteLLM database.
2. Make sure that the LiteLLM config has the model alias that your Agents use. The sample Agent uses `local-agent`. Refer to `values-ollama.yaml` for a local Ollama model.
3. Make the namespace:

   ```sh
   kubectl create namespace koodam-system
   ```

4. Make the Secret with the LiteLLM master key. Read the key from a file, so that the key is not in your shell history:

   ```sh
   kubectl -n koodam-system create secret generic koodam-gateway \
     --from-file=master-key=./master-key
   ```

5. Install the chart. Set the gateway address in `gateway.url`:

   ```sh
   helm install koodam charts/koodam -n koodam-system \
     -f charts/koodam/values-ollama.yaml \
     --set image.repository=<image> --set image.tag=<tag>
   ```

6. Apply the sample Agent and examine its status:

   ```sh
   kubectl apply -f config/samples/core_v1alpha1_agent.yaml
   kubectl get agent fix-tests -o jsonpath='{.status.phase}'
   ```

## Values

| Value | Default | Description |
| --- | --- | --- |
| `image.repository`, `image.tag` | `controller`, `latest` | The controller image. |
| `gateway.mode` | `existing` | The only mode now. A bundled gateway is not in this version. |
| `gateway.url` | `http://litellm.koodam-system.svc:4000` | The LiteLLM admin address for the controller. |
| `gateway.agentUrl` | empty | The gateway address for the agent pods. If empty, the pods use `gateway.url`. |
| `gateway.masterKeySecret.name`, `.key` | `koodam-gateway`, `master-key` | The Secret with the master key. |
| `keyGrace` | `1m` | The time that the controller adds to `deadlineSeconds` for the key TTL. |
| `leaderElection` | `true` | Use leader election. |
| `metrics.enabled` | `false` | Add the HTTPS metrics endpoint and its RBAC. |

## Keep the chart in sync

Do not edit `crds/` or `files/` by hand. These are copies of the generated files in `config/`. After you change the API or an RBAC marker, run:

```sh
make helm-sync
```

CI runs `make helm-check`. The check fails if the chart is not in sync with `config/`. Then it lints and renders the chart.
