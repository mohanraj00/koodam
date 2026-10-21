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

`koodam login codex` stores nothing. Codex keeps 1 login for each Workspace in its home.

1. Connect to the Workspace:

   ```sh
   koodam ssh main -n dev-sample
   ```

2. Install Codex in `~/.local/bin` of the Workspace. See [Install the harnesses](workspaces.md#install-the-harnesses). The controller does not install it yet (issue #98).
3. Log in to Codex in the Workspace:

   ```sh
   codex login --device-auth
   ```

To log out, run `codex logout` in the Workspace.

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

The command refuses these files:

- a file that is not UTF-8 text;
- a credential file, for example `.credentials.json`, `auth.json`, `*.key`, `*.pem`, `id_*`, `.env`, `.netrc`, or a file with a private key;
- a total size above 900 KiB.

It skips symlinks, and shows each skipped path. So it skips a skill folder that is a symlink. It does not replace a `koodam-globals` ConfigMap that it did not make, except with `--force`. You need `get`, `create` and `update` on configmaps in the namespace.

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
