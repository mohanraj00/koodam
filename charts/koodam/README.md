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

4. Make the Secret with the LiteLLM master key. The command makes a random key and gives it to `kubectl` on stdin. Thus the key is not in your shell history, and no key file stays on the disk:

   ```sh
   printf 'sk-%s' "$(openssl rand -hex 32)" | kubectl -n koodam-system \
     create secret generic koodam-gateway --from-file=master-key=/dev/stdin
   ```

   Do not use `echo` for the key. `echo` adds a newline at the end, and the newline goes into the Secret. Then LiteLLM refuses each key with `401 token_not_found_in_db`.

5. Install the chart. Set the gateway address in `gateway.url`. The existing mode has no default address. If `gateway.url` is empty, the install fails. `values-ollama.yaml` sets the address:

   ```sh
   helm install koodam charts/koodam -n koodam-system \
     -f charts/koodam/values-ollama.yaml
   ```

   Without `values-ollama.yaml`, add `--set gateway.url=<address>`.

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
     -f charts/koodam/values-bundled.yaml
   ```

3. Set the `model` of your Agent to `mock-1`, or add your models to `gateway.bundled.litellm.modelList`.

### Upstream provider keys

A real model needs an upstream key, for example `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`. Put the key in a Secret that you make. The chart gives the key to LiteLLM as an env variable with `secretKeyRef`. The chart does not make or read this Secret.

1. Make the Secret. `read -rs` reads the key and does not show it. Type or paste the key, then push Enter:

   ```sh
   read -rs KEY
   printf %s "$KEY" | kubectl -n koodam-system create secret generic upstream-keys \
     --from-file=anthropic=/dev/stdin
   unset KEY
   ```

   The key goes to `kubectl` on stdin. Thus the key is not in the shell history or the process list. Do not use `--from-literal` for a key, because the key then goes into the shell history and the process list.

2. Add the env variables and the models to a values file, for example `upstream.yaml`:

   ```yaml
   gateway:
     bundled:
       litellm:
         extraEnvFromSecrets:
         - name: ANTHROPIC_API_KEY
           secretName: upstream-keys
           secretKey: anthropic
         modelList:
         - model_name: claude
           litellm_params:
             model: anthropic/claude-sonnet-4-5
             api_key: os.environ/ANTHROPIC_API_KEY
   ```

3. Install the chart with `-f charts/koodam/values-bundled.yaml -f upstream.yaml`.

Do not put a key value in `modelList`. Helm writes `modelList` into a ConfigMap and the release values. Use `os.environ/<NAME>` for each `api_key`.

Each item of `extraEnvFromSecrets` needs `name`, `secretName` and `secretKey`. The chart has no field for a literal value. The `name` must not be `LITELLM_MASTER_KEY`, `POSTGRES_PASSWORD` or `DATABASE_URL`, because the chart sets these variables. If you change a key in the Secret, restart LiteLLM with `kubectl rollout restart deployment/<fullname>-litellm`.

### Other bundled settings

The controller uses the bundled LiteLLM Service `<fullname>-litellm`. LiteLLM and the controller read the same master key Secret.

The mock model `mock-1` is off by default. If `gateway.bundled.mock.enabled` is true, LiteLLM answers `mock-1` itself with `mock_response`. The model has a cost per token, so the spend of a key is more than zero. Use it for tests only.

The PostgreSQL password:

- If `gateway.bundled.postgres.passwordSecret.name` is empty, the chart makes the Secret `<fullname>-postgres` with a random password. On upgrade, the chart keeps the password. The chart does not print the password.
- Else, make the Secret before the install. Use only URL-safe characters, because LiteLLM puts the password in the database URL.

The PostgreSQL data is in an `emptyDir` by default. If the pod stops, the keys and the spend are lost. For data that stays, set `gateway.bundled.postgres.persistence.enabled` to `true`. `helm uninstall` deletes the PVC and the chart-made Secret.

Licenses: LiteLLM uses the MIT License. PostgreSQL uses the PostgreSQL License.

## The controller image

The controller image is `ghcr.io/mohanraj00/koodam`. If `image.tag` is empty, the chart uses the chart `appVersion` as the tag. To use an image that you build, set `image.repository` and `image.tag`:

```sh
make docker-build IMG=<image>:<tag>
helm install koodam charts/koodam -n koodam-system -f charts/koodam/values-bundled.yaml \
  --set image.repository=<image> --set image.tag=<tag>
```

## Values

| Value | Default | Description |
| --- | --- | --- |
| `image.repository`, `image.tag` | `ghcr.io/mohanraj00/koodam`, empty | The controller image. If the tag is empty, the chart uses the chart `appVersion`. |
| `gateway.mode` | `existing` | `existing` or `bundled`. |
| `gateway.url` | empty | The LiteLLM admin address for the controller. The existing mode needs it. The bundled mode ignores it. |
| `gateway.agentUrl` | empty | The gateway address for the agent pods. If empty, the pods use `gateway.url`. |
| `gateway.masterKeySecret.name`, `.key` | `koodam-gateway`, `master-key` | The Secret with the master key. |
| `gateway.bundled.litellm.image` | `ghcr.io/berriai/litellm-database:v1.104.2` | The LiteLLM image. |
| `gateway.bundled.litellm.modelList` | `[]` | The LiteLLM `model_list`. |
| `gateway.bundled.litellm.extraEnvFromSecrets` | `[]` | The env variables for the upstream keys. Each item has `name`, `secretName` and `secretKey`. |
| `gateway.bundled.litellm.batchWriteSeconds` | `1` | The LiteLLM `proxy_batch_write_at`. |
| `gateway.bundled.mock.enabled` | `false` | Add the mock model `mock-1`. |
| `gateway.bundled.postgres.image` | `docker.io/library/postgres:16-alpine` | The PostgreSQL image. |
| `gateway.bundled.postgres.passwordSecret.name`, `.key` | empty, `password` | The Secret with the database password. If empty, the chart makes it. |
| `gateway.bundled.postgres.persistence.enabled` | `false` | Use a PVC, not an `emptyDir`. |
| `keyGrace` | `1m` | The time that the controller adds to `deadlineSeconds` for the key TTL. |
| `jobTTLSeconds` | `86400` | The time in seconds that Kubernetes keeps a run Job and its pods (and the log) after the Job ends. The value must be from `60` to `2147483647`. |
| `leaderElection` | `true` | Use leader election. |
| `metrics.enabled` | `false` | Add the HTTPS metrics endpoint and its RBAC. |

## Keep the chart in sync

Do not edit `crds/` or `files/` by hand. These are copies of the generated files in `config/`. After you change the API or an RBAC marker, run:

```sh
make helm-sync
```

CI runs `make helm-check`. The check fails if the chart is not in sync with `config/`. Then it lints and renders the chart.
