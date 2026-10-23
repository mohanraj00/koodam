# Workspaces

A Workspace is a long-lived pod for 1 developer. You connect to it with SSH, and you run the harnesses in `tmux`. A session stays after a disconnect.

The maintainer checked the apps on 2026-10-21 (#15):

- The Claude desktop app connects over SSH, and a task runs.
- The Claude desktop app needs no login in the pod.
- After a cut of the tunnel, the app connected again, and the task continued.
- After `koodam profile push`, the shared skills and `CLAUDE.md` show in the app.
- For the Codex app, add `codex` to `spec.harnesses` and sign in to Codex in the pod.

For each Workspace, the controller makes these objects:

- A StatefulSet with 1 pod, `<workspace>-0`. The pod runs sshd as the user `dev` (uid 1000) on port 2222. The pod passes the Pod Security `restricted` profile.
- A PVC for the home `/home/dev`, with the name `home-<workspace>-0`. The size is `spec.storage`.
- The Secret `<workspace>-ssh-host` with the SSH host key, and the ConfigMap `<workspace>-ssh` with the `authorized_keys`.
- The Secret `<workspace>-gateway`, only if the Workspace has `spec.gateway`.

The pod has no public port. `koodam tunnel` connects SSH to port 2222 through the Kubernetes API, with your kube context and your RBAC. [images/workspace/README.md](../images/workspace/README.md) has the full pod contract.

## Before you start

- **Make the namespace.** This guide uses the namespace `dev-sample` of the [quick start](quickstart.md#3-run-1-agent). If it does not exist, make it:

  ```sh
  kubectl create namespace dev-sample
  ```

- **Bind the namespace to a Profile.** The controller runs a Workspace only in a namespace that a Profile binds. If not, the Workspace stays `Pending`, and the condition `ProfileBound` is `False`. A cluster admin applies a Profile that permits `dev-sample`:

  ```sh
  kubectl apply -f - <<'EOF'
  apiVersion: core.koodam.dev/v1alpha1
  kind: Profile
  metadata:
    name: profile-sample
  spec:
    namespaces:
    - dev-sample
  EOF
  ```

  Add the Profile label to the namespace. [profiles.md](profiles.md) has more:

  ```sh
  kubectl label namespace dev-sample koodam.dev/profile=profile-sample
  ```

- **Get the Workspace image.** A Workspace with no `spec.image` uses the chart value `workspace.image`. The default is `ghcr.io/mohanraj00/koodam-workspace`, with the chart `appVersion` as the tag. A new GHCR package is private. The pod can pull the image only after the maintainer makes the package public.
- **Get the RBAC.** For `koodam ssh setup`, you need `get` and `patch` on workspaces in the namespace. For the tunnel, you need `get` on pods and `create` on pods/portforward.

## Make a Workspace

Write `workspace.yaml`:

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: Workspace
metadata:
  name: main
  namespace: dev-sample
spec:
  storage: 20Gi
```

`koodam ssh setup` adds your public key to `spec.sshPublicKeys`, so you do not have to write it. The field accepts public keys only. The API server refuses a private key.

These fields are optional:

| Field | Description |
|---|---|
| `sshPublicKeys` | The public keys that can log in. Each item is 1 `authorized_keys` line. |
| `image` | The container image. If it is empty, the pod uses the default image of the chart. |
| `storage` | The size of the home PVC. The default is `20Gi`. |
| `suspend` | If `true`, the controller blocks the gateway key and stops the pod. The PVC stays. |
| `gateway.model`, `gateway.budgetPerDay` | A gateway key for the pod, for 1 model, with a budget for each day. Without `gateway`, the harnesses use the subscription login only. |
| `harnesses` | The harnesses in the pod (`claude-code`, `codex`), each with an optional `version` and `libc` (`glibc` or `musl`, default `glibc`). See [The harnesses](#the-harnesses). |

1. Apply the Workspace:

   ```sh
   kubectl apply -f workspace.yaml
   ```

2. Wait for the phase `Running`:

   ```sh
   kubectl wait workspace/main -n dev-sample --for=jsonpath='{.status.phase}'=Running --timeout=5m
   ```

The phase is `Pending`, `Running`, `Suspended` or `Failed`. If the phase is not `Running`, read the conditions with `kubectl describe workspace main -n dev-sample`.

## The harnesses

The controller installs each harness of `spec.harnesses` at each pod start. An init container downloads the binary from the vendor and checks its sha256, as for an Agent run. The binaries are in `/koodam/bin`, read-only. The image has links to them in `/usr/local/bin`, so each SSH command finds them, also with no login shell.

```yaml
spec:
  harnesses:
    - name: claude-code
    - name: codex
      version: 0.47.0
```

- If `version` is empty, the pod gets the latest version at each start. Set `version` to keep the same version after a restart.
- A change to `spec.harnesses` restarts the pod. The home PVC stays.
- If the download or the sha256 check fails, the pod does not start. Read the log with `kubectl logs -n dev-sample main-0 -c harness-claude-code`.
- In a login shell, a harness that you install in `~/.local/bin` has priority, because `~/.profile` puts `~/.local/bin` first.
- If the controller has no installer image, the condition `Harnesses` is `False` with the reason `NoInstallerImage`, and the pod has no harness.

## Secrets in the pod template

`spec.podTemplate` can refer to a Secret in the namespace of the Workspace or the Agent. The controller makes the pod, so the pod can read the Secret, also if your own account cannot read it. The controller refuses these references:

- a Secret with a name that starts with `kd-`. These Secrets hold the gateway keys of the Agent runs.
- a Secret that a Koodam object owns, for example `<workspace>-gateway` and `<workspace>-ssh-host` of a different Workspace.

The Workspace gets `Ready=False` and the Agent gets `Provisioned=False`, with the reason `InvalidPodTemplate`.

**Limit.** The controller checks the owner of a Secret only when the Secret exists. A reference to a Secret that does not exist is permitted, because you can make your Secret later. Thus a template can refer to the name of a Secret that Koodam makes later for a new Workspace in the same namespace. The pod then gets the data of that Secret. If more than one person uses a namespace, do not give a Workspace a name that a different template refers to. Use one namespace for each person.

## Connect with SSH

1. Configure the connection. You can run the command again:

   ```sh
   koodam ssh setup main -n dev-sample
   ```

   The command does these steps:

   1. If `~/.ssh/koodam_ed25519` does not exist, it makes the key with `ssh-keygen`.
   2. It adds the public key to `spec.sshPublicKeys` of the Workspace.
   3. It writes the host key from the Workspace status to `~/.ssh/koodam_known_hosts`.
   4. It writes the host entry to `~/.ssh/config.d/koodam`.
   5. It asks before it adds `Include config.d/koodam` at the top of `~/.ssh/config`. Use `-y` to add the line with no question.

   koodam never reads the private key.

2. Test the connection:

   ```sh
   koodam ssh test main -n dev-sample
   ```

   The test shows the versions of `tmux`, `git` and each harness in the pod. It fails if `tmux` or `git` is not in the pod.

3. Connect:

   ```sh
   koodam ssh main -n dev-sample
   ```

   The command runs `ssh` and attaches to the tmux session `main`. If the session does not exist, tmux makes it.

If a step fails, run `koodam ssh doctor main -n dev-sample`. It checks the ssh program, the `Include` line, the file modes, the kube context, the RBAC for the port-forward and the phase of the Workspace. Without a Workspace name, it checks each Workspace in `~/.ssh/config.d/koodam`.

## The host name

The host name of a Workspace is `koodam-<workspace>.<namespace>`, for example `koodam-main.dev-sample`. If only 1 Workspace in `~/.ssh/config.d/koodam` has the name `<workspace>`, the short name `koodam-<workspace>` also works. Each SSH client that reads `~/.ssh/config` can use these names:

```sh
ssh koodam-main.dev-sample
```

The host entry uses the user `dev`, the key `~/.ssh/koodam_ed25519`, the file `~/.ssh/koodam_known_hosts` and `ProxyCommand koodam tunnel <namespace>/<workspace>`.

## Use tmux

`koodam ssh` always attaches to the session `main`. Start the harness in tmux. If the connection stops, the harness continues in the pod. To attach to the same session again, run `koodam ssh main -n dev-sample`.

To use more sessions, run `tmux` commands in the pod. sshd permits 10 sessions on each connection (`MaxSessions 10` in [images/workspace/sshd_config](../images/workspace/sshd_config)).

## Connect the Claude desktop app

1. Run `koodam ssh setup main -n dev-sample`.
2. Run `koodam ssh test main -n dev-sample`.
3. In the Claude desktop app, add an SSH connection to a remote host.
4. Use the host name `koodam-main.dev-sample`. The app reads the host entry from `~/.ssh/config`.

## Connect the Codex app

1. Run `koodam ssh setup main -n dev-sample`.
2. Run `koodam ssh test main -n dev-sample`.
3. Connect with `koodam ssh main -n dev-sample`. The Workspace must have `codex` in `spec.harnesses`. See [The harnesses](#the-harnesses).
4. Store the shared Codex login with `koodam login codex --stdin -n dev-sample`. See [The shared login](profiles.md#codex). A new Workspace gets this login before sshd starts. An existing Workspace with no local login gets it at the next SSH login shell. A new pod can use the stored login while it is valid. A refresh in another Workspace or on the source machine can make it stale. See [Refresh with a shared login](profiles.md#refresh-with-a-shared-login). The login file stays writable so Codex can refresh it.

5. In the Codex app, add an SSH connection with the host name `koodam-main.dev-sample`.

## Suspend and delete

- **Suspend.** Set `spec.suspend` to `true`. The controller blocks the gateway key first, then it stops the pod. The PVC stays. To start the pod again, set `spec.suspend` to `false`.
- **Delete.** Run `kubectl delete workspace main -n dev-sample`. If the Workspace has a gateway key, a finalizer keeps the Workspace until the controller blocks the key. The home PVC stays. If you do not need the files, delete the PVC yourself.
- **Remove the SSH entry.** `koodam ssh remove main -n dev-sample` removes the host entry and the host key. With `--key`, it also removes your public key from the Workspace. It does not remove the `Include` line or the key files.
