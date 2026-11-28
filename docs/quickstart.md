# Install a release

Release: v0.6.0.

This page installs the release chart and the release `koodam` CLI on your cluster. The [README quickstart](../README.md#quickstart) does the same on kind from a clean checkout, and builds the CLI from the source.

You need a Kubernetes cluster, `kubectl`, `helm` and `openssl`.

## 1. Install the chart

1. Set `VERSION` to the release tag, without the `v`:

   ```sh
   VERSION=0.6.0
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

Do step 4 of the [README quickstart](../README.md#quickstart), with `koodam` in place of `bin/koodam`. Then read [agents.md](agents.md).
