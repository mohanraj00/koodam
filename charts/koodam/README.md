# Koodam Helm chart

This chart installs the Koodam controller and the Agent CRD. The controller uses a LiteLLM gateway. The chart has 2 gateway modes:

- `existing`: the controller uses a LiteLLM gateway that you install.
- `bundled`: the chart also installs LiteLLM and PostgreSQL.

## What the chart installs

- The Agent CRD (from `crds/`).
- The controller Deployment and its ServiceAccount.
- The `manager-role` ClusterRole: the rules that the reconciler needs.
- The `agent-ceiling` ClusterRole: the most that a run Role can grant.
- The leader election Role, in the release namespace.
- If `metrics.enabled` is true: the metrics Service and the metrics-auth ClusterRole.
- If `gateway.mode` is `bundled`: the LiteLLM Deployment, ConfigMap and Service (port 4000), and the PostgreSQL Deployment and Service. If you do not give a password Secret, the chart also makes the PostgreSQL password Secret.

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

## Install with the bundled gateway

1. Make the namespace and the master key Secret, as in steps 3 and 4 above. The master key must start with `sk-`.
2. Install the chart with `values-bundled.yaml`:

   ```sh
   helm install koodam charts/koodam -n koodam-system \
     -f charts/koodam/values-bundled.yaml \
     --set image.repository=<image> --set image.tag=<tag>
   ```

3. Set the `model` of your Agent to `mock-1`, or add your models to `gateway.bundled.litellm.modelList`.

The controller uses the bundled LiteLLM Service `<fullname>-litellm`. LiteLLM and the controller read the same master key Secret.

The mock model `mock-1` is off by default. If `gateway.bundled.mock.enabled` is true, LiteLLM answers `mock-1` itself with `mock_response`. The model has a cost per token, so the spend of a key is more than zero. Use it for tests only.

The PostgreSQL password:

- If `gateway.bundled.postgres.passwordSecret.name` is empty, the chart makes the Secret `<fullname>-postgres` with a random password. On upgrade, the chart keeps the password. The chart does not print the password.
- Else, make the Secret before the install. Use only URL-safe characters, because LiteLLM puts the password in the database URL.

The PostgreSQL data is in an `emptyDir` by default. If the pod stops, the keys and the spend are lost. For data that stays, set `gateway.bundled.postgres.persistence.enabled` to `true`. `helm uninstall` deletes the PVC and the chart-made Secret.

Licenses: LiteLLM uses the MIT License. PostgreSQL uses the PostgreSQL License.

## Values

| Value | Default | Description |
| --- | --- | --- |
| `image.repository`, `image.tag` | `controller`, `latest` | The controller image. |
| `gateway.mode` | `existing` | `existing` or `bundled`. |
| `gateway.url` | `http://litellm.koodam-system.svc:4000` | The LiteLLM admin address for the controller. The bundled mode ignores it. |
| `gateway.agentUrl` | empty | The gateway address for the agent pods. If empty, the pods use `gateway.url`. |
| `gateway.masterKeySecret.name`, `.key` | `koodam-gateway`, `master-key` | The Secret with the master key. |
| `gateway.bundled.litellm.image` | `ghcr.io/berriai/litellm-database:v1.104.2` | The LiteLLM image. |
| `gateway.bundled.litellm.modelList` | `[]` | The LiteLLM `model_list`. |
| `gateway.bundled.litellm.batchWriteSeconds` | `1` | The LiteLLM `proxy_batch_write_at`. |
| `gateway.bundled.mock.enabled` | `false` | Add the mock model `mock-1`. |
| `gateway.bundled.postgres.image` | `docker.io/library/postgres:16-alpine` | The PostgreSQL image. |
| `gateway.bundled.postgres.passwordSecret.name`, `.key` | empty, `password` | The Secret with the database password. If empty, the chart makes it. |
| `gateway.bundled.postgres.persistence.enabled` | `false` | Use a PVC, not an `emptyDir`. |
| `keyGrace` | `1m` | The time that the controller adds to `deadlineSeconds` for the key TTL. |
| `leaderElection` | `true` | Use leader election. |
| `metrics.enabled` | `false` | Add the HTTPS metrics endpoint and its RBAC. |

## Keep the chart in sync

Do not edit `crds/` or `files/` by hand. These are copies of the generated files in `config/`. After you change the API or an RBAC marker, run:

```sh
make helm-sync
```

CI runs `make helm-check`. The check fails if the chart is not in sync with `config/`. Then it lints and renders the chart.
