# The egress span contract (v2)

The egress proxy writes 1 JSON line to its stdout for each call from an Agent pod or a Workspace pod. This line is a span. This page is the contract for the programs that read the spans, for example `koodam trace` and Nookku.

> Status: v0.3.0 and later. The proxy writes `v` and `port` from #168, and the `tls` kind from #166.

## Example

```json
{"v":2,"time":"2026-10-23T09:14:02.118Z","durationMs":412.7,"source":"10.244.1.17","kind":"connect","host":"api.github.com:443","sni":"api.github.com","port":"443","status":200,"bytesIn":5120,"bytesOut":918,"result":"ok","namespace":"dev","pod":"dev-0","workspace":"dev","user":"mohan"}
```

## Fields

| Field | Type | Always | Value |
|---|---|---|---|
| `v` | number | yes | The contract version. It is `2` for this page. |
| `time` | string | yes | The start of the call, RFC 3339 in UTC. |
| `durationMs` | number | yes | The time of the call in milliseconds. |
| `source` | string | yes | The IP address of the pod that made the call. |
| `kind` | string | yes | The path of the call. See "Kinds". |
| `method` | string | no | The HTTP method. `CONNECT` for the kind `connect`. A method that is not standard is `OTHER`. |
| `host` | string | no | The target. For `connect`, it is the `host:port` of the CONNECT request. For `http`, it is the URL host. For `llm`, it is the gateway host. |
| `sni` | string | no | The TLS server name from the ClientHello. Only for `connect` and `tls`. |
| `port` | string | no | The target port, for example `"443"`. |
| `path` | string | no | Only for `llm`. A known gateway route, for example `/llm/v1/messages`, or `/llm/` for a different path. It has no query string. |
| `status` | number | no | The HTTP status that the pod got. `200` for a CONNECT tunnel that opened. |
| `bytesIn` | number | yes | The bytes from the target to the pod. |
| `bytesOut` | number | yes | The bytes from the pod to the target. |
| `result` | string | yes | `ok`, `denied` or `error`. See "Results". |
| `reason` | string | no | Why the call was denied or failed. See "Reasons". |
| `namespace` | string | no | The namespace of the source pod. |
| `pod` | string | no | The name of the source pod, or `unknown`. |
| `agent` | string | no | The value of the pod label `koodam.dev/agent`. |
| `workspace` | string | no | The value of the pod label `koodam.dev/workspace`. |
| `run` | string | no | The value of the pod label `koodam.dev/run`. |
| `user` | string | no | The value of the pod label `koodam.dev/user`, or `unknown`. |
| `app` | string | no | The value of the pod label `koodam.dev/app`. Only for an App pod. |
| `appVersion` | string | no | The value of the pod label `koodam.dev/app-version`. Only for an App pod. |
| `claimed` | boolean | no | `true` if the pod sent an `X-Koodam-Run` or `X-Koodam-User` header. The span does not hold the value. |

"Always" means that the field is in each span. A field that is not always there is not in the line when it is empty.

## Kinds

| Kind | The call | 1 span for each |
|---|---|---|
| `connect` | An HTTPS call through `HTTPS_PROXY` (a CONNECT tunnel). | TLS connection |
| `tls` | An HTTPS call that DNS steering sent to the proxy (#166). | TLS connection |
| `http` | A plain HTTP call. | HTTP request |
| `llm` | A model call to `<proxy>/llm/`, which the proxy sends to the gateway. | HTTP request |

The proxy does not decrypt TLS. For `connect` and `tls`, the span has the host and the SNI, but no method, path or status of the requests inside the tunnel. A client can send many requests on 1 TLS connection, so 1 span can hold many requests.

## Results

| Result | Meaning |
|---|---|
| `ok` | The proxy sent the call to the target. For `http` and `llm`, `status` is the status of the target. |
| `denied` | The proxy refused the call. The pod got `403`, or the proxy closed the tunnel. |
| `error` | The call failed after the proxy permitted it, for example the target did not answer. |

## Reasons

| Reason | Result | Meaning |
|---|---|---|
| `port` | `denied` | The port is not in the permitted ports. |
| `address` | `denied` | The name resolves to a private, loopback, link-local or metadata address. |
| `identity` | `denied` | The proxy found no pod, or more than 1 pod, with the source IP. |
| `not a proxy request` | `denied` | The request is not a CONNECT, an absolute URL or an `/llm/` path. |
| `no TLS ClientHello` | `denied` | The first bytes in a tunnel are not a TLS ClientHello. |
| `the SNI is not the CONNECT host` | `denied` | The SNI is not the name of the CONNECT request. |
| `no SNI` | `denied` | A `tls` call has no SNI, so the proxy has no target. |
| `connection limit` | `denied` | The TLS listener has too many open connections, in total or from the source. |
| `idle` | `error` | No byte moved for the idle limit (`--idle-timeout`, 10 minutes by default). The proxy stopped the call. |
| `upstream` | `error` | The proxy could not connect to the target, or the target failed. |
| `hello timeout` | `error` | The pod sent no ClientHello in the time limit. |
| `TenantTokenNoKey` | `denied` | The proxy has no tenant token key for the AppVersion of the pod. |
| `TenantTokenMissing` | `denied` | An `llm` call of an App pod has no tenant token. |
| `TenantTokenInvalid` | `denied` | The tenant token is not a token of the AppVersion of the pod: it has a false signature, or it is not a token. |
| `TenantTokenExpired` | `denied` | The tenant token is at its end time or after it. |
| `TenantLabelNotASCII` | `denied` | The tenant label of the token cannot be the value of a header. |

A reader must accept other reason text. The reason is for people, and new reasons can come in the same version.

## The tenant token

The proxy can bind each `llm` call of an App pod to a tenant. Then it accepts such a call only with a tenant token in the header `Koodam-Tenant-Token`. The claim check container of the pod signs this token for the tenant of each request, with a short life.

- The proxy flag `--tenant-tokens` sets this. The proxy then reads the public key of the token from the status of the AppVersion of the pod. The key does not come from the pod. [tenants.md](tenants.md#the-tenant-token) has the full flow.
- The proxy examines the signature and the end time of the token. If the token is not valid, the pod gets `403` with the typed reason in the header `Koodam-Reason`.
- For a valid token, the proxy sets the header `x-litellm-customer-id` of the gateway call to the tenant label of the token. It removes the headers `x-litellm-customer-id` and `x-litellm-end-user-id` that the pod sent. The gateway reads these headers before each field of the body, so it counts the call for the tenant of the token.
- The gateway does not get the token, and no span has the token or the tenant label.
- A tenant label in the header must have only visible ASCII characters, with no space at its start or end.
- A pod that is not an App pod has no tenant. With `--tenant-tokens`, the proxy sets the customer header of its `llm` calls to the reserved customer `koodam-no-tenant`.

## What a span holds from the pod

A span has no request header, no query string and no body. Thus the `Authorization` header, a key in the query string and the request data are never in a span.

An `http` span has no path, because some APIs put a token in the path. An `llm` span holds only a known gateway route, or `/llm/`, because the pod chooses the path. The span records only that a claim header came, not its value (#170).

Only `host` and `sni` hold text that the pod chose. A pod can put data in a host name, thus give the spans the same protection as the logs of the pods: only the people who can read the logs of a Workspace can read its spans.

## Identity

The proxy finds the pod from the source IP of the connection, with a watch on the pods. The identity fields come from the pod labels. The proxy does not trust a header from the pod. If no pod or more than 1 pod has the source IP, the proxy refuses the call with the reason `identity`.

### App pods (#183)

The fields `app` and `appVersion` are the names in the reserved pod labels `koodam.dev/app` and `koodam.dev/app-version`. They are not a session, and they are not a proof of an evaluation. A span with `app` has no `agent`, `workspace` or `run`. `user` keeps its meaning: it is the value of the pod label `koodam.dev/user`, if the App pod has the label.

The proxy accepts an App identity only if the pod has the 2 labels with values, and has no `koodam.dev/agent`, `koodam.dev/workspace` or `koodam.dev/run` label. If the pod has only 1 App label, an empty App label, or an App label and an Agent, Workspace or run label, the proxy refuses the call with the reason `identity`. A header cannot give or change an App identity or the user. The proxy watches the App pods only in the named namespaces of `--namespaces`, with 1 watch of all the pods of each namespace. If the list is empty or has only the empty name of all namespaces, the proxy refuses the calls of all App pods. The watches of all namespaces for the Agent and Workspace pods do not give an App identity.

This is only the span part of #183. The full App wiring in production, default-deny ingress and egress for the App pods, the proxy steering of the App pods, and the Nookku correlation are still necessary before the App integration is complete. Koodam has no App controller and no App NetworkPolicy yet.

## Version rules

- An added field keeps `v` the same. A reader must ignore a field that it does not know.
- A removed field, a renamed field or a new meaning of a field adds 1 to `v`.
- A span with no `v` field is version 1. The proxy of v0.3 before #168 wrote no `v`.
- Version 2 (#170) replaced `claimedRun` and `claimedUser` with `claimed`, and removed `path` from `http` spans.

## Traffic with no span

These calls do not go through the proxy, so they have no span:

- a call to 127.0.0.1 inside the pod;
- a call to the Kubernetes API server;
- a call to a peer in `workspace.networkPolicy.extraEgress`, for example an in-cluster Service.

## Read the spans

`koodam trace` (#144) reads the logs of the proxy pods with your kubeconfig. Your account needs `list` on `pods` and `get` on `pods/log` in the namespace of the proxy. `--json` prints each span without change.

```sh
koodam trace dev --json --since 2026-10-23T09:00:00Z --until 2026-10-23T10:00:00Z > spans.jsonl
```

A span is in the window if `since <= time < until`.

## Release samples

Each release has 3 sample files as assets. A consumer, for example Nookku, can test its reader on real lines with no cluster.

| File | Content | Format |
|---|---|---|
| `egress-spans.jsonl` | The spans of one Agent and one Workspace, from `koodam trace <target> --json`. | This contract. Each line has `v` (version 2 for this contract). |
| `gateway-log.jsonl` | The spend logs of the key of the same Agent, from `GET /spend/logs?api_key=<key hash>` of the gateway. | 1 log object on each line, in the format of the bundled LiteLLM image of the chart. |
| `app-ingress-spans.jsonl` | The App ingress spans of the thin App test, from the log of the container `koodam-claim`. | [app-ingress-spans.md](app-ingress-spans.md). Each line has `v` (version 1 for that contract). |

The release workflow makes the files. Its job `samples` runs the egress e2e tests and the App e2e tests on kind with the tagged commit. A person does not write the files. Before the e2e test writes a file, it checks the file:

- Each line is a JSON object.
- No line holds the master key, a word with the key prefix `sk-`, a bearer token or a signed token (JWT). The App sample also holds no claim token of the test. If a check fails, the test writes no file, and the release stops.
- `api_key` in a gateway log is the SHA-256 hash of the key, not the key.

The spans have the source IP addresses, the pod names and the namespaces of the kind run. The gateway logs have the key alias of the run.

`checksums.txt` of the release has the SHA-256 of each sample. To download and check the samples of a release, set `TAG` to the release tag, then run:

```sh
gh release download "$TAG" --repo mohanraj00/koodam --pattern '*.jsonl' --pattern checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

A release before the samples job has no sample files. A release before #345 has no `app-ingress-spans.jsonl`.
