# The ingress span contract (v1)

The ingress proxy (#147) writes 1 JSON line to its stdout for each completed handler call. This line is an ingress span. This page is the contract for the programs that read ingress spans. It is separate from the [egress span contract](egress-spans.md). Ingress spans do not change a field, a meaning or a reader of egress v2.

> Status: v0.4, in build. The record, the mapping, the encoder and the stdout writer are in `internal/ingress` (`span.go`, `span_writer.go`). No ingress handler, Deployment, Service or wiring uses them yet. Thus no proxy writes ingress spans yet, and no runtime test proves this page. Unit tests cover the helpers only.

## Line format

- 1 UTF-8 JSON object on 1 line, with a final newline and no other data.
- An optional field is not in the line when it has no proved value. The proxy never writes `null`, an empty string or `0` to show "unknown".
- A reader must ignore a field that it does not know.

## Fields

The line has these 22 fields and no other field.

| Field | Type | Presence | Value |
|---|---|---|---|
| `v` | integer | required | `1`. |
| `kind` | string | required | `ingress`. |
| `time` | string | required | Wall clock at handler entry. RFC 3339 in UTC with a `Z` suffix and 0 to 9 fraction digits. |
| `traceID` | string | optional | 32 lower-case hex characters, not all zero. |
| `proxyNamespace` | string | required | The namespace of the ingress proxy, from trusted startup configuration. |
| `targetNamespace` | string | group T | The namespace of the target Agent. |
| `targetAgent` | string | group T | The Agent `metadata.name`. |
| `targetAgentUID` | string | group T | The Agent `metadata.uid`. |
| `targetRun` | string | group T | The name of the Agent's current Job (`status.job`). It is not an App session. |
| `targetRunUID` | string | group T | The Job `metadata.uid`. |
| `targetPod` | string | group T | The name of the 1 selected Ready pod. |
| `targetPodUID` | string | group T | The pod `metadata.uid`. |
| `callerNamespace` | string | group C | The namespace part of `system:serviceaccount:<namespace>:<name>`. |
| `callerServiceAccount` | string | group C | The name part of the same username. |
| `callerUID` | string | group C | The ServiceAccount UID. |
| `method` | string | required | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS` or `OTHER`. |
| `status` | integer | optional | The final HTTP status that the handler selected. |
| `bytesOut` | integer | required | Request body bytes that the proxy read from the caller. `0` if no read ran. |
| `bytesIn` | integer | required | Backend response body bytes that the proxy read. `0` if no read ran. |
| `durationMs` | number | required | Elapsed time of the handler call in milliseconds. Finite and nonnegative. |
| `result` | string | required | `ok`, `denied` or `error`. |
| `reason` | string | conditional | A fixed reason. Required when `result` is not `ok`. Never present when `result` is `ok`. |

Groups:

- **Group T.** The 7 target fields are all in the line, or none of them are.
- **Group C.** The 3 caller fields are all in the line, or none of them are.

Names are Kubernetes names: namespaces are DNS-1123 labels, other names are DNS-1123 subdomains. A UID is the opaque value that the API returned (`status.user.uid` or `metadata.uid`), kept as it is. The encoder refuses an empty UID, a UID that is not valid UTF-8 and a name with a different form. This check does not prove where a value came from.

`bytesIn`, `bytesOut`, `durationMs`, `status`, `result` and `reason` have the egress v2 names, but this page gives their ingress meaning. A reader must not apply egress v2 meanings to them.

## Provenance

A format check cannot prove where a value came from. The encoder checks only the form and the presence rules. The handler must obey these rules:

- **Caller (group C).** Only from a successful TokenReview, as `status.user.username` and `status.user.uid`. Never from a route segment, a claim header, `Forwarded`, `X-Forwarded-*` or another header. Groups and `extra` are not written. Group C is absent on the proxy `401` (`reason` `authentication`), on a review API failure before TokenReview succeeded, and when the call stops before TokenReview. This rule applies only to the proxy `401`. It does not apply to a backend `401`. A completed exchange with a backend `401` keeps group C, because TokenReview proved the caller before the exchange. Caller data still comes only from the successful TokenReview, never from the backend status or the backend response. A SubjectAccessReview refusal keeps group C, because TokenReview proved the caller.
- **Target (group T).** Only from a successful final uncached check of the Agent, the Job and the pod. The values come from that final check, not from an earlier lookup. The route namespace and Agent name are caller input and are never target fields. There is no fallback to route values. Group T is absent on each proxy refusal and each proxy error that stops the call before the final check succeeded: the proxy `400`, `401`, `403`, `405`, `404` and `503` in "Results". This rule applies only to the codes that the proxy sends. It does not apply to backend statuses. A completed exchange keeps group T and has `result` `ok` for each backend status that the proxy forwarded, for example `400`, `401`, `403`, `404`, `405`, `500` or `503`. An `upstream` `502`, a cancellation or a stream failure after the final check keeps group T. A stream failure or a cancellation after the handler selected a backend status also keeps that status.
- **Trace.** Only from a successful approved trace helper (`ReadTrace`). It is the trace ID of the `traceparent` that the proxy forwards or would forward. It is absent if the helper did not run, if the trace header was malformed or repeated, or if the random source failed. The proxy never copies part of an invalid header and never makes a second trace context for the log. The parent ID, the flags, `tracestate` and `baggage` are not in the span. A retained trace ID is a value that the caller chose. It can carry 128 bits of caller data. It does not prove a caller, a session or a request.
- **Status.** Only the final code that the handler actually selected and passed to the response writer: a proxy refusal code from "Results", or the backend status that the proxy forwarded. Informational responses do not set it. After the handler selects a status, the span keeps it. If the handler selected no status, the field is absent. The encoder never makes a status for the span: the fixed code in "Results" is what the handler sends, not a value that the span fills in. For a refusal or an `upstream` failure, a selected status that is not the fixed code makes the record invalid. The proxy never writes a response to a caller that went away only to get a status. `status` does not prove that the caller got the response.
- **Bytes.** Local reads, counted by a counter that is safe for concurrent use. `bytesOut` is caller to backend, `bytesIn` is backend to caller. If the transport decodes content, the count is of decoded bytes. Headers, trailers, chunk framing, TLS records, 1xx responses, proxy error bodies and bytes that `net/http` reads outside the body reader are not counted. The counts are not wire bytes and not delivery counts. On a refusal both counts are `0`. On `HEAD`, `bytesIn` is `0`.
- **Duration.** The monotonic clock, from handler entry to span assembly. It excludes the TLS handshake and the header read. The encoder refuses a time with no monotonic reading and a negative elapsed time. `time` is wall clock and can move with clock changes. A `durationMs` value is a measurement of 1 call, not a benchmark result.

**Target IP reuse limit.** Group T records the Agent, Job and pod objects at the final check. It does not prove which process got the bytes. The pod IP can go to a different pod between the final check and the connect. Each container in the pod shares the trust unit and can listen on the port. Group T is not a cryptographic workload identity.

Caller identity is access-controlled log data. Give the ingress proxy logs the same protection as other audit logs.

## Data that is never in a span

No URL, path, query, host header, request or response header, request body, response body, token, `Authorization` value, certificate, credential hash, TokenReview or SubjectAccessReview body, groups, `extra`, claim value, raw Go error, backend error text or writer error. The only text values are constants, validated Kubernetes names and UIDs, the closed method set, the fixed reasons and a validated trace ID. A method outside the set is `OTHER`. The raw method is never written.

## Results

| `result` | Meaning |
|---|---|
| `ok` | The proxy got the backend response headers, read the backend body to its end without error, and each write to the caller returned no error. The backend status does not change this. `ok` does not prove that the caller got the bytes. |
| `denied` | The proxy refused malformed input, or a completed review refused the caller. |
| `error` | Any other stop. |

In this table, a fixed `status` is the code that the handler sends. If the handler selected no status, the field is absent.

| `reason` | `result` | `status` | When |
|---|---|---|---|
| `route` | `denied` | `400` | Malformed route, path, encoding, names or authority form. |
| `trace` | `denied` | `400` | Malformed or repeated trace header. |
| `method` | `denied` | `405` | A method outside the 7 approved methods, `CONNECT` or a protocol upgrade. |
| `authentication` | `denied` | `401` | A missing, conflicting or refused Bearer credential, or a completed TokenReview that fails the caller rules. |
| `authorization` | `denied` | `403` | A completed SubjectAccessReview that is not `allowed=true`, or that has an evaluation error. |
| `review` | `error` | `503` | A TokenReview or SubjectAccessReview API call failed or timed out. |
| `entropy` | `error` | `503` | The random source failed when the trace helper needed new IDs. |
| `target` | `error` | `404` | The target helper returned `ErrTargetAbsent`. |
| `target` | `error` | `503` | The target helper returned `ErrTargetUnavailable`. |
| `upstream` | `error` | `502` | Connect or backend failure before the response headers, and the request context had no error. |
| `stream` | `error` | the selected status | The backend body read or a caller write failed after the handler selected the status, and the request context had no error. |
| `cancelled` | `error` | the selected status, or absent | The exchange failed or stopped before normal completion, and the request context had an error at classification. |

Rules:

- A completed backend `400`, `401`, `403`, `404`, `405`, `500` or `503` is `ok` with no `reason`, and keeps group T. A proxy `404` is `error` with `reason` `target`. A proxy `503` is `error` with `reason` `review`, `entropy` or `target`, and has no group T. A reader separates them by `result` and `reason`, never by `status`.
- `target` is the only reason of the target helper. The span does not split it into policy, readiness or API causes.
- **Cancellation.** The handler reads the request context error once, when it classifies a call that failed or stopped before normal completion. A non-nil error then gives `cancelled`. A later cancellation never changes a completed `ok` exchange. A completed refusal keeps its own reason, also when the context has an error. The proxy does not claim to know which of 2 concurrent errors came first. Cancellation before the response headers gives no `status`, and the proxy writes no response. Cancellation after the headers keeps the selected status. A cancelled call keeps each group (trace, caller, target) whose helper succeeded before the stop, and omits each group whose helper did not run. Cancellation after the target helper returned a resolved target keeps group T. Body counts are `0` without group T.
- A selected status does not change. A `2xx` `status` with `result` `error` is valid: the headers went out, but the exchange did not complete.
- A reader must treat an unknown `reason` as unknown, never as success. A later revision can add a fixed reason without a change to `v`.

## Emission and its limits

- The handler assembles the span at the end of the call, encodes it, and makes 1 write attempt to stdout of the proxy container. Lines from different calls do not interleave: 1 worker writes each line with 1 `Write` call.
- **Encoding failure.** A record that breaks a rule on this page gives 1 fixed error with no input text, and no line.
- **Write failure.** A failed write, a short write, a timeout or a full admission gives 1 fixed error inside the process. The raw writer error is never exposed. The proxy writes no stdout or stderr diagnostic and no log line. It does not retry, write a second span, fall back to another writer or add a sink. After a failed or short write, the writer refuses all later lines, because stdout can hold a partial line.
- **Backpressure.** A caller waits a bounded time. Lines do not wait in a queue: a line that the worker cannot take in the wait limit is dropped. Go cannot stop a `Write` call that blocks. Then at most 1 worker goroutine and 1 line stay until `Write` returns. Close does not wait for that `Write`. After close, each new write fails before admission and the worker cannot claim a job. The claim marks the start of the write. A job that the worker claimed before close can enter or complete the sink call after close.
- A write failure cannot undo the HTTP exchange. The response that the caller got stays the same.
- **No durable audit guarantee.** A write failure, a full pipe, a crash, an out-of-memory kill, a pod delete, node loss, a panic before span assembly or log rotation can lose a span. A line does not prove that the exchange completed. A missing line does not prove that no request came.
- No metric, event, status field or other sink gets span data.

Implementation limits (in `span_writer.go`, not contract values or settings): at most 64 callers wait at the same time, and each caller waits at most 250 ms. A change to these limits does not change this contract.

## Read the spans

The ingress proxy runs in its own Deployment with the `ingress-proxy` component label. Egress span readers do not select it. A reader must identify ingress lines by `kind` `ingress` and by the source component, not by `v` alone, because egress v2 reads a line with no `v` as egress version 1.

`targetRun` is the Job name. It is not the egress v2 `run` field, which is the pod label `koodam.dev/run`. Do not join the 2 fields without a defined mapping.

## Nookku

Ingress spans are supplemental metadata for Nookku. Nookku keeps its own HTTP tap for input, reply and body audit. A span gives no tap row, input, reply, seal, body digest, source equivalence or correlation proof, and Nookku must not make these from a span. A retained `traceID` is chosen by the caller and is not a correlation proof. Koodam does not add bodies to its logs for Nookku. Nookku field mapping, a conformance test, a source identity rule and any span-to-tap correlation are not done. They are necessary before a claim that Nookku integrates Koodam ingress.

## Version rules

- An added field or an added fixed reason keeps `v` the same.
- A removed field, a renamed field or a new meaning of a field adds 1 to `v`.
