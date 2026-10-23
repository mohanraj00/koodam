# The Codex sandbox in a Workspace

The Codex bubblewrap sandbox did not start with the tested restricted pod settings, under either runc or gVisor. Codex itself started and printed its version. Do not treat a running Codex process as proof that its tool sandbox works.

The [result file](results/codex-sandbox-72.json) records the commands, exit codes, output, binary hashes and test versions. The test used Linux arm64, Codex 0.163.0 and gVisor release-20261019.0 with the systrap platform on kind.

| Check | runc | gVisor RuntimeClass |
| --- | --- | --- |
| Pod starts with restricted Pod Security | Yes | Yes |
| `codex --version` | Pass | Pass |
| bubblewrap with a user namespace | Refused | Pass |
| bubblewrap with user and network namespaces | Refused | Loopback setup fails |
| `codex sandbox` with a local read command | Namespace setup fails | Loopback setup fails |
| `codex sandbox` with `sandbox_mode="workspace-write"` | Namespace setup fails | Loopback setup fails |

Under runc, bubblewrap reports `No permissions to create a new namespace`. The node permits unprivileged user namespaces, but this pod cannot make one. The error alone does not show which runtime or host policy refused it.

Under gVisor, a user namespace works. With a network namespace, bubblewrap reports `loopback: Failed RTM_NEWADDR: No child process`. The same error stops both Codex commands before the test command runs. The plain shell control works, and the test files stay unchanged after the sandbox checks. gVisor does not make this Codex sandbox usable with the tested settings.

## Scope and safe use

The probes use the current Workspace image and verified Codex release binaries. The test pods have the security context of a Workspace: user and group 1000, no capabilities, no privilege escalation and `RuntimeDefault` seccomp. The namespace enforces Pod Security `restricted`. The gVisor pod selects a real `runsc` handler. Its kernel output confirms gVisor; this is not a RuntimeClass that maps to runc.

The home is an empty `emptyDir`. No user home, login Secret, provider key or ServiceAccount token is mounted. These are local commands, with no model call. The test does not cover SSH, tmux, PVC storage, model calls, the code-mode host or a complete agent run. It does not test a cloud cluster, Linux amd64, Kata or a different gVisor platform.

Keep the restricted pod settings. Do not add `SYS_ADMIN`, a privileged container or an unconfined seccomp profile to make this check pass. Do not turn off the Codex sandbox as a response to this error. No safe workaround was verified in this test. Check the sandbox again when the runtime or Codex version changes, before you use it for agent tool commands.

## Repeat the check

Use a disposable kind cluster named `koodam-72`. Give kind an explicit `--kubeconfig` path in a local scratch folder. Do not use a real cluster or mount a real home. The script always selects the context `kind-koodam-72` from the file that you give it.

1. Build `images/workspace` as `koodam-workspace:72-sandbox`.
2. Make empty scratch folders `tools` and `probe-image`, then set `probe_image_folder` to the latter. Build and run the Koodam harness installer. It checks the release signatures and checksums. The container gets no host home or inherited key variables:

   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
     -o "$scratch_folder/tools/harness-install" ./cmd/harness-install
   docker run --rm --user "$(id -u):$(id -g)" \
     -v "$scratch_folder/tools:/tools:ro" -v "$probe_image_folder:/koodam/bin" \
     --entrypoint /tools/harness-install koodam-workspace:72-sandbox \
     -harness codex -version 0.163.0 -dir /koodam/bin
   ```

   The test image uses only `.codex-real`, `bwrap` and `codex-code-mode-host` from this output. Do not copy a home folder or a login file.
3. Add this Dockerfile to that folder, build the image, and load it into kind:

   ```dockerfile
   FROM koodam-workspace:72-sandbox
   COPY --chmod=0755 .codex-real bwrap codex-code-mode-host /koodam/bin/
   USER 1000:1000
   ```

   ```sh
   docker build -t koodam-codex-probe:72 "$probe_image_folder"
   kind load docker-image koodam-codex-probe:72 --name koodam-72
   ```

4. Install the official gVisor bundle in the kind node, with its published checksum check. Keep `runsc`, `containerd-shim-runsc-v1` and `gvisor-bin/` together in `/usr/local/bin`. This test used the [point release](https://github.com/google/gvisor/releases/tag/release-20261019.0). Do not install it on a production node for this check.
5. Add this handler to the kind node's containerd config, then restart containerd in that node:

   ```toml
   [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
     runtime_type = "io.containerd.runsc.v1"
     [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc.options]
       TypeUrl = "io.containerd.runsc.v1.options"
       ConfigPath = "/etc/containerd/runsc.toml"
   ```

   Put this in `/etc/containerd/runsc.toml`:

   ```toml
   [runsc_config]
     platform = "systrap"
   ```

   This block is for the version 2 config format that the tested kind image uses. Read the [gVisor containerd guide](https://gvisor.dev/docs/user_guide/containerd/quick_start/) and the [installation guide](https://gvisor.dev/docs/user_guide/install/) for other versions. Do not replace the whole containerd config with this block.
6. Run the probe from the repo root:

   ```sh
   python3 test/sandbox/probe.py --kubeconfig "$test_kubeconfig" \
     --image koodam-codex-probe:72 --output "$scratch_folder/results.json"
   ```

   It makes the restricted namespace, the RuntimeClass and both test pods. Exit 0 means that the recording finished. Read the exit code of each probe in the result file; a recorded sandbox failure is not a sandbox pass.
7. Delete the test cluster with `kind delete cluster --name koodam-72`. The test runtime exists only in that node.
