# Tenants

A tenant is a client group of an App. The tenant label comes from the signed claim that the claim check of the App verifies ([apps.md](apps.md#the-signed-claim-check)). It does not come from a ServiceAccount. A `TenantQuota` gives one tenant label a quota for each period and a rate limit. Koodam uses the tenant label only for the quota, the rate limit and the spend group. The application isolates the data of its tenants.

## What is active

- The `TenantQuota` API is in `main`. The chart installs its CRD with `apps.enabled=true`.
- The claim check container can refuse a tenant from a limits file. See "The refusals of the claim check".
- The gateway client can make the customer of a tenant label, read its use and delete it. See "The gateway customer".
- With a tenant claim, the tenant token binds each model call of an App pod to the tenant of a request. See "The tenant token".
- No controller reads a `TenantQuota` at this time, so no pod gets a limits file and the gateway gets no customer. The controller, the status and the default for an unknown tenant come in later changes of [#193](https://github.com/mohanraj00/koodam/issues/193).

## The TenantQuota spec

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: TenantQuota
metadata:
  name: acme
spec:
  tenant: acme
  appRef:
    name: shop
  period: Day
  requests: 10000
  spend: "5"
  rateLimit:
    requestsPerSecond: "5"
    burst: 10
```

| Field | Rule |
|---|---|
| `tenant` | Necessary. The tenant label of the signed claim, with 1 to 253 characters. You cannot change it after create. |
| `appRef.name` | Optional. The App that the rate limit and the claim check refusals apply to. With no value, they apply to each App of the namespace. |
| `period` | `Hour` or `Day`. The default is `Day`. The gateway sets the start of each period. |
| `requests` | Optional. The maximum count of model calls of the tenant in one period. The minimum is 1. |
| `spend` | Optional. The maximum spend of the tenant in one period, in the currency of the gateway price table. It must be more than zero. |
| `rateLimit.requestsPerSecond` | Necessary with `rateLimit`. A quantity more than zero, for example `"5"` or `"500m"`. |
| `rateLimit.burst` | Optional. The count of requests that the tenant can send at one time. With no value, it is the rate, with a minimum of 1. |

A `TenantQuota` with no limit only makes the tenant label a known tenant.

The gateway has one customer for each tenant label of the install. Thus use one `TenantQuota` for each tenant label. A `TenantQuota` sets the gateway budget of its tenant label for the full install, so give the right to create one only to the operator of the install.

[config/samples/core_v1alpha1_tenantquota.yaml](../config/samples/core_v1alpha1_tenantquota.yaml) is an example.

## The status

| Field | Meaning |
|---|---|
| `conditions` | `Ready` shows that the gateway has the customer of the tenant with the limits of the spec. `QuotaExceeded` shows that the tenant is at a limit of the current period. |
| `periodEnd` | The time when the gateway starts the next period of the tenant. |
| `requests` | The count of model calls of the tenant in the current period. |
| `spend` | The spend of the tenant in the current period. |
| `observedTime` | The time of the last read of the use from the gateway. |

## The refusals of the claim check

The claim check container `koodam-claim` reads the limits of the tenants from a file, and it reads the file again each 10 seconds. A file that is not valid keeps the last valid limits. The container does not start with a file that is not valid.

The check examines a request after it verified the signed claim:

| Reason | Status | Condition |
|---|---|---|
| `UnknownTenant` | `403` | The file does not name the tenant label, and the file refuses an unknown tenant. |
| `TenantQuotaExceeded` | `429` | The file marks the tenant at its limit of the current period. |
| `TenantRateLimited` | `429` | The token bucket of the tenant is empty. The header `Retry-After` gives the seconds until the next token. |

- The answer has the reason in the header `Koodam-Reason` and in the body. The app container does not get a refused request.
- The ingress span of the request has the reason as its `outcome`, with the caller label and the tenant label ([app-ingress-spans.md](app-ingress-spans.md)).
- Each pod has its own token bucket for each tenant. A refused request uses no token, and the limit of one tenant has no effect on a different tenant.
- A request with no tenant label has no tenant limit. This occurs only if the claim check has no tenant claim.

## The gateway customer

The quota for each period is in the gateway. The gateway has one customer for each tenant label, with these values from the `TenantQuota`:

| Gateway value | Source |
|---|---|
| The budget of the period | `spend`. With no value, the customer has no spend limit. |
| The length of the period | `period`. A period of an hour starts at the full hour. A period of a day starts at 00:00 UTC. |
| The model calls for each minute | `rateLimit.requestsPerSecond` times 60. With no value, the customer has no rate. |

- The egress proxy sets the customer of each model call of an App pod from the tenant token of the call. Then the gateway counts the call for the customer. See "The tenant token".
- With `apps.claimCheck.tenantToken=false`, the app sends the tenant label of the header `Koodam-Tenant` as the `user` field of each model call. The gateway then trusts the label that the code in the pod sends, and a call with no `user` field is not in the count of a tenant.
- The gateway refuses a model call of a customer at its budget with the error type `budget_exceeded`. This is the refusal `TenantQuotaExceeded` of the gateway. It refuses a call above the rate with the status 429.
- The spend is the count of the gateway for the current period. The requests are the successful model calls of the tenant label in the spend log, from the start of that period.
- The gateway stops the count of the spend log at a maximum (`SPEND_LOGS_PAGINATION_COUNT_CAP` in LiteLLM), and then it sets the flag `total_is_capped`. The client gives this flag with the count. A count with the flag is a minimum, not an exact count.
- Koodam owns one LiteLLM budget for each tenant label, with the ID `kd-tenant-` and a hash of the label. It changes no other budget. A customer that the gateway already has keeps its spend when it gets this budget.
- The tenant label is in the URL of 2 read calls to the gateway, so it can be in the access log of the gateway. The label is not a credential.

## The tenant token

The code in an App pod is not trusted. Thus the gateway must not count a model call for a tenant label that this code selects. With a tenant claim (`apps.claimCheck.tenantClaim`), the tenant token binds each model call of an App pod to the tenant of a request. The chart value `apps.claimCheck.tenantToken` sets it, and the default is `true`.

1. The controller makes an Ed25519 key for each AppVersion. The private seed is in the Secret `kd-<version>-tenant-token`, and only the claim check container mounts it. The public key is in `status.tenantTokenKey` of the AppVersion.
2. For each request with a tenant label, the claim check container signs a token with the label and an end time. The app container gets the token in the header `Koodam-Tenant-Token`. The token has a life of 10 minutes from the start of the request.
3. The app sends the header `Koodam-Tenant-Token` with each model call.
4. The egress proxy reads the public key from the status of the AppVersion of the pod. It examines the signature and the end time of the token.
5. For a valid token, the proxy sets the header `x-litellm-customer-id` of the gateway call to the tenant label of the token. It removes the customer headers that the pod sent. The gateway reads this header before each field of the body, so a `user` field of the app has no effect on the count.

If the token is not valid, the app gets the status `403` from the proxy. The header `Koodam-Reason` and the egress span have the typed reason:

| Reason | Meaning |
|---|---|
| `TenantTokenMissing` | The model call has no tenant token. |
| `TenantTokenInvalid` | The token is not a token of the AppVersion of the pod: it has a false signature, or it is not a token. |
| `TenantTokenExpired` | The token is at its end time or after it. |
| `TenantTokenNoKey` | The status of the AppVersion has no key. The controller writes the key before it makes the pods. |
| `TenantLabelNotASCII` | The tenant label cannot be the value of a header. |

- An app can make a model call only for a request with a tenant label. A model call with no request of a tenant gets the refusal `TenantTokenMissing`.
- A tenant label must have only visible ASCII characters, with no space at its start or end.
- Each model call of an Agent pod or a Workspace pod gets the reserved customer `koodam-no-tenant`. Thus the code in these pods cannot make the gateway count a call for a tenant. Do not use this label for a tenant.
- The gateway does not get the token, and no span has the token or the tenant label.

This is a change from v0.4.0 for an install with a tenant claim: the app must send the header `Koodam-Tenant-Token` with each model call. With `apps.claimCheck.tenantToken=false`, the install has the behavior of v0.4.0.

The tenant token has these limits:

- An app can use the token of a tenant for a model call of a different request, until the end time of the token. It cannot use the label of a tenant that sent no request to it in that time.
- A principal that can create pods or read Secrets in the namespace of the App can read the seed. It can then sign a token for each tenant label in that namespace.
- The proxy finds the pod of a call from the source IP and the pod labels. A principal that can change the labels of a pod can change the identity of the pod.
- The proxy must be on the path of each model call. A gateway of the mode `existing` at a public address, or an `extraEgress` rule that opens the gateway, lets a pod call the gateway directly. The gateway then trusts the label that the pod sends.
- After the loss of the Secret of the seed, the version gets a new key and new pods. The proxy refuses the model calls of the old pods with `TenantTokenInvalid` until they stop.
