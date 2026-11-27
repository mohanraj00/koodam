# One trace for a run (design)

> Status: design for v0.6. No code uses these rules yet. The build is [#446](https://github.com/mohanraj00/koodam/issues/446). The decision is in [#188](https://github.com/mohanraj00/koodam/issues/188).

This page gives the rules that join the spans of 1 run, or of 1 request, in 1 trace. The spans come from the ingress proxy, the gateway, the tools of a harness and the egress proxy. The [egress span contract](egress-spans.md) and the [ingress span contract](ingress-spans.md) stay the contracts of the JSON spans. This page adds 2 optional fields to the egress span and an optional OTLP export.

## What Koodam can see

| Source | Can the proxy read `traceparent`? | Note |
|---|---|---|
| Egress `llm` call | Yes | A plain HTTP request to the proxy, which the proxy sends to the gateway. |
| Egress `http` call | Yes | A plain HTTP request. |
| Egress `connect` or `tls` call | No | The proxy does not decrypt TLS. TLS termination is [#195](https://github.com/mohanraj00/koodam/issues/195). |
| Ingress | Yes | The ingress span already has `traceID` ([ingress-spans.md](ingress-spans.md)). The ingress proxy is [#147](https://github.com/mohanraj00/koodam/issues/147). |
| Gateway | Yes | LiteLLM can export an OTel span for each model call, and it reads an incoming `traceparent`. |
| Tool call | No | A tool call runs in the harness, in the pod. Koodam sees only its egress. |

## Trace ID rules

The egress span gets 2 optional fields: `traceID` (32 lower-case hex characters, not all zero) and `spanID` (16 lower-case hex characters, not all zero). The proxy sets them in this order:

1. **Valid header.** If an `llm` or `http` call has 1 valid `traceparent`, the span uses its trace ID. The span ID is new, and the parent is the span ID of the header.
2. **Run trace.** If a call from an Agent pod or a Workspace pod has no valid `traceparent`, the span uses the run trace ID. The run trace ID is the first 16 bytes of SHA-256 of the UID of the Agent run or the Workspace, in lower-case hex. Thus all spans of a run join in 1 trace, also the `connect` and `tls` spans.
3. **App call.** An App call with no valid `traceparent` has no trace ID.
4. **Bad header.** A malformed or repeated `traceparent` counts as no valid header. The proxy never copies part of an invalid header.

The span never holds `tracestate` or `baggage`, because they can hold data.

## The gateway call

On an `llm` call with no valid `traceparent`, the proxy adds a `traceparent` with the run trace ID and the span ID of its own span. Thus the gateway span joins the run trace. The proxy sends a valid header from the pod without change. The proxy adds a header only on an `llm` call, never on a call to the internet.

The chart gets an optional OTel callback for the LiteLLM gateway. It is off by default.

## Export

- The JSON log stays the contract, and it is always on.
- An optional OTLP exporter in the egress proxy sends the same spans as OTel spans. It is off by default.
- The endpoint comes only from the chart values, never from a pod. The NetworkPolicy of the proxy permits only that endpoint.
- A failed export never changes a call and never stops the JSON log.
- The operator installs the collector, for example with the OTel Operator. Koodam does not bundle a collector.

## Tool spans

The chart gets an optional value that sets `OTEL_EXPORTER_OTLP_ENDPOINT` and `TRACEPARENT` (the run trace) in the run pods. A harness that reads them can join its tool spans to the run trace.

**Set the tenant attributes of a tool span in the collector, from the pod metadata.** A pod can write any resource attribute, so a tool span from a pod does not prove its tenant. The `k8sattributes` processor of the OTel collector can set the namespace and the pod labels.

## Data that is never in a span

An OTel span holds no credential, no claim value, no header value and no URL path. It does not hold the gateway path of an `llm` JSON span. It holds the kind, the host, the status, the result, the reason and the tenant labels of the JSON span.

## Trust

- A trace ID from a pod is a value that the pod chose. It can carry 128 bits of data. It does not prove a tenant, a run or a request.
- A pod can put the trace ID of a different tenant in its header. Its spans then show in that trace, but with its own tenant labels. Filter a trace by the tenant labels before you trust it.
- The tenant labels of a Koodam span come only from the pod labels, never from a header.
- The run trace ID is not a secret. A user who knows the run UID can compute it. It gives no access.

## Limits

- A `connect` or `tls` span covers 1 TLS connection, which can hold many requests. It joins the run trace, but not the trace of 1 request.
- An App call with no `traceparent` has no trace.
- The ingress part needs the ingress proxy ([#147](https://github.com/mohanraj00/koodam/issues/147)).
- A span is not a durable audit record. The limits of the [egress span contract](egress-spans.md) apply.
