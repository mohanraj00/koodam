# Quick start

This guide installs the chart and the `koodam` CLI. Then it runs 1 Agent against the mock model of the bundled gateway. The mock model needs no upstream key.

You need a Kubernetes cluster, `kubectl`, `helm` and `openssl`. A local cluster, for example kind, is enough.

## 1. Install the chart

1. Set `VERSION` to the release tag, without the `v`:

   ```sh
   VERSION=<version>
   ```

2. Make the namespace of the controller:

   ```sh
   kubectl create namespace koodam-system
   ```

3. Make the Secret with the LiteLLM master key. The command gives a random key to `kubectl` on stdin. So the key is not in your shell history:

   ```sh
   printf 'sk-%s' "$(openssl rand -hex 32)" | kubectl -n koodam-system \
     create secret generic koodam-gateway --from-file=master-key=/dev/stdin
   ```

   Do not use `echo` for the key. `echo` adds a newline, and then LiteLLM refuses each key.

4. Install the chart with the bundled gateway:

   ```sh
   helm install koodam oci://ghcr.io/mohanraj00/charts/koodam --version "$VERSION" -n koodam-system \
     -f "https://raw.githubusercontent.com/mohanraj00/koodam/v$VERSION/charts/koodam/values-bundled.yaml"
   ```

`values-bundled.yaml` installs LiteLLM and PostgreSQL, and adds the mock model `mock-1`. The mock model is for tests only. For real models and for the gateway mode `existing`, see [gateway.md](gateway.md) and [charts/koodam/README.md](../charts/koodam/README.md).

## 2. Install the CLI

The CLI is for linux and darwin, on amd64 and arm64.

1. Download the archive and the checksum file:

   ```sh
   OS=linux     # or darwin
   ARCH=amd64   # or arm64
   FILE="koodam_${VERSION}_${OS}_${ARCH}.tar.gz"
   curl -fsSLO "https://github.com/mohanraj00/koodam/releases/download/v$VERSION/$FILE"
   curl -fsSLO "https://github.com/mohanraj00/koodam/releases/download/v$VERSION/checksums.txt"
   ```

2. Check the archive:

   ```sh
   grep " $FILE\$" checksums.txt | shasum -a 256 -c -
   ```

   If the command does not show `OK`, stop. Do not install the file.

3. Install the CLI:

   ```sh
   tar -xzf "$FILE" koodam
   sudo install koodam /usr/local/bin/koodam
   koodam version
   ```

The CLI uses your kubeconfig. The flags `-n`, `--context` and `--kubeconfig` work as in `kubectl`.

## 3. Run 1 Agent

1. Make a namespace for the run:

   ```sh
   kubectl create namespace dev-sample
   ```

2. Write the Agent to `agent.yaml`. The script calls the model `mock-1` 1 time through the gateway. It uses the env variables `OPENAI_BASE_URL` and `OPENAI_API_KEY`, which the controller gives to the pod. It does not write the key value:

   ```yaml
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
   ```

3. Run the Agent:

   ```sh
   koodam run -f agent.yaml --wait
   ```

   The command creates the Agent and shows the pod log. Then it waits for the revoke. At the end, it shows the spend. The pod log shows `200`, the HTTP status of the call.

4. Look at the result:

   ```sh
   koodam get -n dev-sample hello
   koodam spend -n dev-sample hello
   ```

After the run, the controller blocks the gateway key. Then it deletes the ServiceAccount, the Role and the RoleBinding of the run. The Agent keeps the status and the spend.

## Next steps

- [workspaces.md](workspaces.md): a long-lived pod that you connect to with SSH.
- [gateway.md](gateway.md): real models, the budget and the spend.
