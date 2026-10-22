# Architecture

Koodam has 5 parts: the controller, the gateway, the Agent Job, the Workspace pod with its PVC home, and the CLI with the tunnel.

## The controller

The controller is a Kubernetes operator. The chart installs it with the CRDs `Agent`, `Workspace` and `Profile`. It has 3 reconcilers:

- **Agent.** For each Agent, it makes a gateway key, a ServiceAccount, a Role, a RoleBinding, a Secret and a Job in the namespace of the Agent. The `agent-ceiling` ClusterRole is the most that a run Role can grant. When the Job ends, or when you delete the Agent, the controller blocks the key first. Then it deletes the access, reads the spend and writes it to the status. A finalizer keeps the Agent until the revoke is done.
- **Workspace.** For each Workspace in a bound namespace, it makes these objects: the SSH host key Secret, the `authorized_keys` ConfigMap and a StatefulSet with 1 pod. With `spec.gateway`, it also makes the gateway key Secret. It renews the gateway key. When you suspend, unbind or delete the Workspace, it blocks the key first.
- **Profile.** It reads the Profiles and the Namespaces, and writes the bound namespaces to the Profile status. It writes nothing else.

The controller writes no credential value to a log, an event or the status. The status has the key alias and the SHA-256 hash of the key.

## The gateway

The gateway is LiteLLM. The chart uses an existing LiteLLM, or installs its own with PostgreSQL. The controller uses the master key to make, renew and block the keys, and to read the spend. Each key permits 1 model and has a budget and a TTL. [gateway.md](gateway.md) has the details.

## The Agent Job

An Agent is 1 Job that runs to the end. The pod gets the key and the gateway address as env, and the token of the run ServiceAccount. If the Agent has a harness, an init container downloads the harness binary from the vendor and checks its sha256. The controller deletes the ended Job after the chart value `jobTTLSeconds`.

## The Workspace pod and the PVC home

A Workspace is a StatefulSet with 1 pod. The pod runs sshd as uid 1000 on port 2222, with public key login only. It passes the Pod Security `restricted` profile and has no ServiceAccount token.

The pod mounts these sources:

| Path | Source |
|---|---|
| `/home/dev` | The PVC `home-<workspace>-0`. It stays when you suspend, unbind or delete the Workspace. |
| `/etc/koodam/ssh` | The `authorized_keys` ConfigMap and the host key Secret. |
| `/etc/koodam/globals` | The ConfigMap `koodam-globals` of `koodam profile push`, optional. |
| `/etc/koodam/login` | The Secret `koodam-login` of `koodam login`, optional. |
| `/etc/koodam/gateway` | The gateway key Secret, only with `spec.gateway`. |

The gateway key is a file and not an env variable, because sshd does not give the container env to an SSH session. A login shell exports the variables from the files.

## The CLI and the tunnel

The pod has no Service and no public port. `koodam tunnel NAMESPACE/WORKSPACE` connects stdin and stdout to port 2222 of the pod through the Kubernetes API, with a port-forward. It uses the kube context and the RBAC of the developer. OpenSSH uses it as the `ProxyCommand` of the host entry that `koodam ssh setup` writes.

```text
ssh client
  -> ProxyCommand: koodam tunnel <namespace>/<workspace>
  -> Kubernetes API (pods/portforward)
  -> pod <workspace>-0, sshd on port 2222
  -> tmux, the harnesses, the home PVC
```

The Claude desktop app works. The Codex app needs Codex in the pod. See [workspaces.md](workspaces.md).
