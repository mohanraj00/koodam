# The koodam CLI

`koodam` uses your kubeconfig and your RBAC. These flags apply to each command, as in `kubectl`:

| Flag | Description |
|---|---|
| `-n`, `--namespace` | The namespace. |
| `--context` | The kubeconfig context. |
| `--kubeconfig` | The path to the kubeconfig file. |

`koodam help <command>` shows the full help of a command.

## Agents

| Command | Description |
|---|---|
| `koodam run -f AGENT.yaml` | Create an Agent. |
| `koodam get [NAME]` | Show the phase, the model, the harness, the budget, the spend, the start and end time, and the key alias. |
| `koodam spend [NAME]` | Show the budget and the gateway usage: the spend, the calls, the failed calls and the tokens. |
| `koodam logs NAME` | Show the pod log of the Job of the Agent. |
| `koodam delete NAME` | Delete an Agent. The controller revokes the key before it deletes the Agent. |

The flags:

- `koodam run --wait`: the command does these steps:
  1. It waits for the pod, then shows the pod log.
  2. It waits for the revoke.
  3. It shows the spend.

  `--timeout` is the maximum time of each wait, for the pod and for the revoke. The default is 1 h.
- `koodam logs -f`: the command does these steps:
  1. It waits for the pod.
  2. It streams the log until the pod stops.

  `--timeout` is the maximum time to wait for the pod. The default is 5 min.
- `koodam delete --wait`: the command waits until the controller revokes the key and deletes the Agent. `--timeout` is the maximum time for `--wait`. The default is 5 min.

If the `-n` of `koodam run` does not agree with the namespace in the file, the command stops.

For `koodam logs` and `run --wait`, you need `get` on jobs, `list` on pods and `get` on pods/log in the namespace.

## Workspaces

| Command | Description |
|---|---|
| `koodam ssh setup WORKSPACE` | Configure the SSH connection. `-y` adds the `Include` line with no question. |
| `koodam ssh test WORKSPACE` | Connect and show the versions of `tmux`, `git` and the harnesses. |
| `koodam ssh doctor [WORKSPACE]` | Find the usual faults of the SSH connection. |
| `koodam ssh WORKSPACE` | Connect with SSH and attach to the tmux session `main`. |
| `koodam ssh remove WORKSPACE` | Remove the host entry and the host key. `--key` also removes your public key from the Workspace. |
| `koodam tunnel NAMESPACE/WORKSPACE` | Connect stdin and stdout to port 2222 of the pod. OpenSSH uses it as a `ProxyCommand`. The argument gives the namespace, so the command does not use `-n`. |
| `koodam usage WORKSPACE` | Show the tokens and the projected API cost of the harness sessions. |

[workspaces.md](workspaces.md) has the steps for these commands. The RBAC:

- `koodam tunnel`: `get` on pods and `create` on pods/portforward in the namespace. It opens no local port.
- `koodam ssh setup` and `koodam ssh remove --key`: `get` and `patch` on workspaces, plus the tunnel verbs.

## Profiles

| Command | Description |
|---|---|
| `koodam login claude-code` | Store a Claude Code token in the Secret `koodam-login`. `--stdin` reads 1 line from stdin. |
| `koodam login codex` | Store nothing. Show the login step in the Workspace. |
| `koodam login HARNESS --remove` | Remove the login of the harness. For `codex`, the command removes nothing and shows the logout step. |
| `koodam profile push` | Push `CLAUDE.md`, `AGENTS.md` and the skill folders to the ConfigMap `koodam-globals`. The flags are `--claude-md`, `--agents-md`, `--skills`, `--dry-run` and `--force`. |

[profiles.md](profiles.md) has the steps for these commands.

## Other

| Command | Description |
|---|---|
| `koodam version` | Show the version of the CLI. |

## The spend and the projected cost

Koodam shows 2 types of cost. They are not the same.

**The gateway spend is the real spend.** An Agent, or a Workspace with `spec.gateway`, gets a gateway key. The gateway counts the spend of the key. For an Agent, the controller writes the spend to the status after the revoke:

```sh
koodam spend fix-tests -n dev-sample
kubectl get agents -n dev-sample
```

For a Workspace with a gateway key, read the gateway usage in the status:

```sh
kubectl get workspace main -n dev-sample -o jsonpath='{.status.usage}'
```

**The projected cost is not a bill.** A Workspace with a subscription login has no gateway spend. `koodam usage` shows the API price of the tokens of its sessions:

```sh
koodam usage main -n dev-sample
```

The command connects with SSH, as `koodam ssh test` does, and runs a fixed script in the pod. The script reads `~/.claude/projects` and `~/.codex/sessions`. It prints only the model names and the token counts, not the text. The CLI multiplies the counts by a pinned subset of the LiteLLM price file ([internal/usage/prices](../internal/usage/prices/README.md)).

The output has 1 line for each harness and model, and a total. The columns are `HARNESS`, `MODEL`, `INPUT`, `OUTPUT`, `CACHE-READ`, `CACHE-WRITE`, `COST` and `SOURCE`. Each line says `source: subscription`, because the plan does not bill this cost. A model with no price shows its tokens and `no price`.

The projected cost gives no budget and no revoke. [gateway.md](gateway.md) tells why to use the gateway, its limits for the projected cost, and how to configure it.
