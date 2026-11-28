# The App ingress span contract (v1)

Release: v0.6.0.

The claim check container `koodam-claim` of an App pod writes 1 JSON line to its stdout for each request to the App. This line is an App ingress span. This page is the contract for the programs that read the spans, for example Nookku.

[apps.md](apps.md) tells how to turn on the claim check. [egress-spans.md](egress-spans.md) is the contract of the egress spans, which record the calls from the App pods. [ingress-spans.md](ingress-spans.md) is a different contract: the spans of the Agent ingress proxy.

## Example

```json
{"v":1,"time":"2026-11-19T09:14:02.118Z","kind":"app-ingress","app":"shop","version":"shop-v1","method":"GET","status":200,"outcome":"ok","caller":"e2e-client","tenant":"team-e2e","durationMs":3.412}
```

## Fields

| Field | Type | Always | Value |
|---|---|---|---|
| `v` | number | yes | The contract version. It is `1` for this page. |
| `time` | string | yes | The start of the request, RFC 3339 in UTC. |
| `kind` | string | yes | Always `app-ingress`. |
| `app` | string | no | The name of the App. |
| `version` | string | no | The name of the AppVersion of the pod. This is not the contract version. |
| `method` | string | yes | The HTTP method: `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` or `OPTIONS`. Each other method is `OTHER`. |
| `status` | number | yes | The HTTP status that the caller got. `401` for a refused claim. `403` for a refused zone. `429` or `403` for a refused tenant. `502` if the app container did not answer. |
| `outcome` | string | yes | `ok`, or the reason of the refusal. See "Outcomes". |
| `caller` | string | no | The caller label of the claim. Only for the outcome `ok`, for a refused zone and for a refused tenant. |
| `tenant` | string | no | The tenant label of the claim. Only for the outcome `ok`, for a refused zone and for a refused tenant, and only if the claim check has a tenant claim. |
| `zone` | string | no | The zone of the install (`zone.name`). It is not the zone of the caller. An install with no zone has no such field. |
| `durationMs` | number | yes | The time of the request in milliseconds, with microsecond steps. |

"Always" means that the field is in each span. A field that is not always there is not in the line when it is empty.

## Outcomes

| Outcome | Status | Meaning |
|---|---|---|
| `ok` | the status of the app | The claim is valid. The app container got the request. |
| `syntax` | `401` | The request has no bearer token, or the token is not a JWT that the check can read. |
| `algorithm` | `401` | The token does not use RS256 or ES256. |
| `key` | `401` | The JWKS has no key for the algorithm and the key ID of the token. |
| `signature` | `401` | The signature is not valid. |
| `issuer` | `401` | The claim `iss` is not the issuer of the check. |
| `audience` | `401` | The claim `aud` does not have the audience of the check. |
| `time` | `401` | The token has no `exp`, it expired, or its `nbf` is in the future. The check permits 30 seconds of clock difference. |
| `caller` | `401` | The caller claim is missing, empty, not printable or longer than 253 characters. |
| `tenant` | `401` | The tenant claim is not valid by the same rule. |
| `ZoneMismatch` | `403` | The claim is valid, and its zone claim is not the zone of the install. With `zone.requireClaim`, a claim with no zone claim gets this outcome too. |
| `TenantRateLimited` | `429` | The claim is valid, and the tenant sent more requests than its rate limit. |
| `TenantQuotaExceeded` | `429` | The claim is valid, and the tenant is at its quota of the current period. |
| `UnknownTenant` | `403` | The claim is valid, no `TenantQuota` names the tenant label, and the install refuses such a tenant. |
| `ReservedTenant` | `403` | The claim is valid, and its tenant label is the reserved label `koodam-no-tenant`. |

A reader must accept other outcome words. New outcomes can come in the same version.

## What a span does not hold

A span has no token, no path, no query string, no header and no body. Thus the `Authorization` header, the signed claim and the request data are never in a span. A request with a refused claim has no `caller` and no `tenant`, because the check does not trust a claim that it refused. A refused zone and a refused tenant have a valid claim, so their spans have the 2 labels. The zone of a refused caller is not in the span. [zones.md](zones.md) has the zone refusal, and [tenants.md](tenants.md) has the tenant refusals.

`caller` and `tenant` are the labels of a valid claim, not the claim. The issuer chooses them, so they can identify a person. Give the spans the same protection as the logs of the App pods: only the people who can read the logs of the namespace can read its spans.

## Read the spans

The spans are in the log of the container `koodam-claim`. To read the spans of the App `shop` in the namespace `dev`, run:

```sh
kubectl -n dev logs -l koodam.dev/app=shop -c koodam-claim --tail=-1 --max-log-requests=10 | grep '^{'
```

The container writes its errors to stderr, and `kubectl logs` shows them too. Only a line that starts with `{` is a span.

## Version rules

- An added field keeps `v` the same. A reader must ignore a field that it does not know.
- A removed field, a renamed field or a new meaning of a field adds 1 to `v`.
- A span with no `v` field is from a claim check before #345. Read it as version 1.

## Release sample

Each release from v0.4.0 has the asset `app-ingress-spans.jsonl`, with its SHA-256 in `checksums.txt`. [egress-spans.md](egress-spans.md) tells how the release workflow makes the samples and how to download and check them.

The sample has the spans of the thin App e2e test on kind: requests with a valid claim, requests with no claim and requests with a bad signature. Before the test writes the file, it checks that each line is a JSON object with `v` and the kind `app-ingress`, and that no line holds the token of the test, the master key, a word with the key prefix `sk-`, a bearer token or a signed token.
