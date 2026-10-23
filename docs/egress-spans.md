# The egress span contract (v1)

The egress proxy writes 1 JSON line to its stdout for each call from an Agent pod or a Workspace pod. This line is a span. This page is the contract for the programs that read the spans, for example `koodam trace` and Nookku.

> Status: v0.3, in build. The proxy writes `v` and `port` from #168. The `tls` kind comes with #166.

## Example

```json
{"v":1,"time":"2026-10-23T09:14:02.118Z","durationMs":412.7,"source":"10.244.1.17","kind":"connect","host":"api.github.com:443","sni":"api.github.com","port":"443","status":200,"bytesIn":5120,"bytesOut":918,"result":"ok","namespace":"dev","pod":"dev-0","workspace":"dev","user":"mohan"}
```

## Fields

| Field | Type | Always | Value |
|---|---|---|---|
| `v` | number | yes | The contract version. It is `1` for this page. |
| `time` | string | yes | The start of the call, RFC 3339 in UTC. |
| `durationMs` | number | yes | The time of the call in milliseconds. |
| `source` | string | yes | The IP address of the pod that made the call. |
| `kind` | string | yes | The path of the call. See "Kinds". |
| `method` | string | no | The HTTP method. `CONNECT` for the kind `connect`. |
| `host` | string | no | The target. For `connect`, it is the `host:port` of the CONNECT request. For `http`, it is the URL host. For `llm`, it is the gateway host. |
| `sni` | string | no | The TLS server name from the ClientHello. Only for `connect` and `tls`. |
| `port` | string | no | The target port, for example `"443"`. |
| `path` | string | no | The URL path. Only for `http` and `llm`. It has no query string. |
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
| `claimedRun` | string | no | The `X-Koodam-Run` header that the pod sent. Do not trust it. |
| `claimedUser` | string | no | The `X-Koodam-User` header that the pod sent. Do not trust it. |

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
| `upstream` | `error` | The proxy could not connect to the target, or the target failed. |
| `hello timeout` | `error` | The pod sent no ClientHello in the time limit. |

A reader must accept other reason text. The reason is for people, and new reasons can come in v1.

## What a span never holds

A span has no request header, no query string and no body. Thus a span cannot hold a gateway key, a token or a password. The claimed fields are the only header values, and the proxy cuts them to a short length.

## Identity

The proxy finds the pod from the source IP of the connection, with a watch on the pods. The identity fields come from the pod labels. The proxy does not trust a header from the pod. If no pod or more than 1 pod has the source IP, the proxy refuses the call with the reason `identity`.

## Version rules

- An added field keeps `v` at `1`. A reader must ignore a field that it does not know.
- A removed field, a renamed field or a new meaning of a field changes `v` to `2`.
- A span with no `v` field is version 1. The proxy of v0.3 before #168 wrote no `v`.

## Traffic with no span

These calls do not go through the proxy, so they have no span:

- a call to 127.0.0.1 inside the pod;
- a call to the Kubernetes API server;
- a call to a peer in `workspace.networkPolicy.extraEgress`, for example an in-cluster Service.

## Read the spans

`koodam trace` (#144) reads the logs of the proxy pods with your kubeconfig. Your account needs `get` on `pods/log` in the namespace of the proxy. `--json` prints each span without change.

```sh
koodam trace dev --json --since 2026-10-23T09:00:00Z --until 2026-10-23T10:00:00Z > spans.jsonl
```

A span is in the window if `since <= time < until`.
