# Profiles, the login, the globals and the memory

A Profile binds namespaces. In a bound namespace, each Workspace gets 3 shared layers:

- the login of a harness, from `koodam login`;
- the global files (`CLAUDE.md`, `AGENTS.md` and the skills), from `koodam profile push`;
- the project files (`AGENTS.md` and `.agents/skills`), from the repo.

The Profile holds no key, no login and no file. The login and the globals are a Secret and a ConfigMap in each namespace.

## Profiles and the namespace binding

A Profile is a cluster resource. A cluster admin writes it. It lists the namespaces that it permits.

The controller binds a namespace to a Profile only if both of these are true:

1. The Profile lists the namespace in `spec.namespaces`.
2. The namespace has the label `koodam.dev/profile=<Profile name>`.

So a namespace owner cannot bind a namespace to a Profile with the label only.

1. Apply the Profile:

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

2. Add the label to the namespace:

   ```sh
   kubectl label namespace dev-sample koodam.dev/profile=profile-sample
   ```

3. Look at the binding:

   ```sh
   kubectl get profiles
   ```

The controller writes the bound namespaces to `status.boundNamespaces`. `kubectl get profiles` shows them in the column `Bound`.

The controller runs a Workspace only in a bound namespace. If you remove the label or the list item, the controller blocks the gateway key of the Workspace first. Then it stops the pod. The home PVC stays.

## The Workspace network

The chart can make a NetworkPolicy for the Workspace pods in each Profile namespace. The chart does not read the Profiles. Thus, add each Profile namespace to the chart values, and run `helm upgrade` after each change. The namespace must exist before the upgrade.

```yaml
workspace:
  networkPolicy:
    namespaces: [dev-sample]
```

The policy permits only this traffic:

| Direction | Permitted |
| --- | --- |
| Ingress | TCP 2222, the sshd port. `koodam tunnel` uses a port-forward to this port. |
| Egress | UDP and TCP 53 to the DNS pods (`workspace.networkPolicy.dns`). |
| Egress | TCP 4000 to the bundled LiteLLM pods, only in the bundled gateway mode. |
| Egress | Each public address, if `publicInternet` is `true`. The harnesses use it for the vendor APIs, the harness downloads, git and the package registries. |
| Egress | The rules of `workspace.networkPolicy.extraEgress`. |

The public internet rule blocks the private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), the shared range `100.64.0.0/10`, the link-local ranges with the cloud metadata address, and the loopback ranges. Thus a Workspace pod cannot reach the API server, the nodes or the other pods of the cluster. If the gateway is not bundled, add an `extraEgress` rule for it. A rule can only use IP ranges, not host names.

> **Caution:** Use a CNI that enforces NetworkPolicy, for example Calico, Cilium or the kindnet of kind. If the CNI does not enforce it, the policy has no effect and the pods can reach each address.

This policy is a basic limit. It does not trace the calls. Traced egress with a proxy comes in v0.3 (#8).

## The shared login

### Claude Code

1. On your computer, make a token with `claude setup-token`.
2. Store the token in the namespace:

   ```sh
   koodam login claude-code -n dev-sample
   ```

   The command reads the token with no echo. To read 1 line from stdin, add `--stdin`. The command never shows the token. It checks only the form of the token.

The command stores the token in the key `claude-code` of the Secret `koodam-login`. Each Workspace pod in the namespace mounts the Secret read-only at `/etc/koodam/login`. A login shell in the pod exports `CLAUDE_CODE_OAUTH_TOKEN` from the file. It does not replace a value that you exported.

`koodam login` does not change a `koodam-login` Secret that it did not make. You need `get`, `create`, `update` and `delete` on secrets in the namespace.

To remove the login:

```sh
koodam login claude-code --remove -n dev-sample
```

The command deletes the key. If the Secret has no other key, the command deletes the Secret. Also revoke the token in your Anthropic account.

### Codex

`koodam login codex --stdin -n dev-sample` reads Codex ChatGPT login JSON from a pipe. It stores the JSON in the key `codex` of `koodam-login`. It checks the format without a provider call. It refuses API keys and never shows the login. Supply the login through a pipe that you control. Do not put it in a command argument, a shell trace or a log.

The Workspace copies `/etc/koodam/login/codex` to `$HOME/.codex/auth.json` before sshd starts and at each new login shell. The folder is private, and the file is writable only by its owner. The copy uses a temporary file and an atomic hard link. It refuses a link at the destination. It keeps an existing local login, including tokens that Codex refreshed. A custom `CODEX_HOME` is not changed. Codex must use file login storage at the default home.

A change to the Secret is used only when the local file is absent. To use a new shared login in an existing Workspace, stop Codex, remove `~/.codex/auth.json` and any `~/.codex/.login.*` files, then open a new SSH login shell after Kubernetes updates the mounted Secret.

To remove the shared login, first revoke it in your OpenAI account. Then run `koodam login codex --remove -n dev-sample` to remove the Secret key. Stop Codex and remove `~/.codex/auth.json` and all `~/.codex/.login.*` files from each Workspace, including suspended Workspaces. Temporary copies can stay after an interrupted copy. A Secret delete does not revoke a login or delete its PVC copies.

#### Refresh with a shared login

The [Codex refresh source](https://github.com/openai/codex/blob/22db5847c834266986a53b782f2de21e8e834e0a/codex-rs/login/src/auth/manager.rs) saves replacement tokens and the refresh time in local storage. It handles expired, reused and revoked refresh tokens as failures that need a new login. The [file storage source](https://github.com/openai/codex/blob/22db5847c834266986a53b782f2de21e8e834e0a/codex-rs/login/src/auth/storage.rs) writes the updated login to the Codex home.

Each Workspace has an independent copy. Koodam does not send refreshed tokens back to the Secret or coordinate refreshes between pods. From the source behavior, the stored login in the Secret can become stale after the first refresh in a Workspace or on the source machine. New Workspaces can then get a stale login and fail when the access token expires. Do not keep the imported login in use on the source machine. Run `koodam login codex --stdin` again with a new login when the stored login becomes stale. Use a separate login for each Workspace if concurrent refresh must be reliable. This result is from source inspection and synthetic tests. No real login or provider call was used. Concurrent provider refresh was not tested.

In v0.2, only Workspaces use the login. An Agent run uses a gateway key.

## The global files

`koodam profile push` copies your global files to the ConfigMap `koodam-globals` in the namespace:

| File | Default source | Flag |
|---|---|---|
| `CLAUDE.md` | `~/.claude/CLAUDE.md` | `--claude-md` |
| `AGENTS.md` | `~/.codex/AGENTS.md` | `--agents-md` |
| The skill folders | `~/.claude/skills` | `--skills` |

1. Do the checks with no cluster call. The command shows the keys and the sizes:

   ```sh
   koodam profile push --dry-run -n dev-sample
   ```

2. Push the files:

   ```sh
   koodam profile push -n dev-sample
   ```

The command stops and pushes nothing in these conditions:

- A file has a credential name or a private key, for example `.credentials.json`, `auth.json`, `*.key`, `*.pem`, `id_*`, `.env` or `.netrc`. The command checks each file, also in a skill that it skips.
- `CLAUDE.md` or `AGENTS.md` is not UTF-8 text, or is larger than 900 KiB.
- The total size is above 900 KiB. The error names the largest skills and their sizes.

The command skips a skill that has a file that is not UTF-8 text or is larger than 900 KiB. It also skips a skill that is larger than 900 KiB, the limit for 1 skill. For each skipped skill, it writes 1 line on stderr, `skip the skill <folder>: <reason>`. Then it pushes the other skills.

It skips dot folders at each level and never reads them. It also skips dot files, symlinks and files that are not regular, and shows each skipped path. So it skips a skill folder that is a symlink. `--dry-run` shows the same skip lines. It does not replace a `koodam-globals` ConfigMap that it did not make, except with `--force`. You need `get`, `create` and `update` on configmaps in the namespace.

Each Workspace pod mounts the ConfigMap read-only at `/etc/koodam/globals`. The script `koodam-globals` copies the files to `~/.koodam/globals` and makes these links:

- `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md`;
- 1 link for each skill in `~/.claude/skills` and `~/.codex/skills`.

If the globals have `AGENTS.md` and no `CLAUDE.md`, Claude Code also reads `AGENTS.md`.

The script runs when the pod starts and at each login. So a new push applies at the next login, with no pod restart. The script never replaces a file, a folder or a link of your own. Do not edit the files under `~/.koodam/globals`, because the next push replaces them.

## The project files

Put the instructions and the skills of a project in its repo:

- **Instructions:** write `AGENTS.md`.
- **Skills:** put them in `.agents/skills/<name>/SKILL.md`.

Codex reads these files. Claude Code reads `CLAUDE.md` and `.claude/skills`. The script `koodam-project` lets Claude Code read the files of Codex:

- If `AGENTS.md` exists and `CLAUDE.md` does not exist, it writes `CLAUDE.md` with the text `@AGENTS.md`.
- If `.agents/skills` exists and `.claude/skills` does not exist, it links `.claude/skills` to `../.agents/skills`.
- In a git work tree, it adds each file that it made to the git exclude file.

It never replaces a file and never fails. In a Workspace, the script runs at each login, in each direct child folder of the home with `AGENTS.md` or `.agents/skills`. To run it in a different folder, run `koodam-project <folder>`. In an Agent run with a harness, the harness wrapper runs the script in the current folder first.

## The memory

In v0.2, each Workspace keeps its memory on its home PVC. The harnesses write their memory in the home, for example in `~/.claude` and `~/.codex`. The PVC stays when the pod restarts, and when you suspend or delete the Workspace. So the memory also stays.

The Workspaces of a namespace do not share their memory. The shared memory is issue #91, for v0.3.
