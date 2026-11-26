# Tenants

A tenant is a client group of an App. The tenant label comes from the signed claim that the claim check of the App verifies ([apps.md](apps.md#the-signed-claim-check)). It does not come from a ServiceAccount. A `TenantQuota` gives one tenant label a quota for each period and a rate limit. Koodam uses the tenant label only for the quota, the rate limit and the spend group. The application isolates the data of its tenants.

## What is active

- The `TenantQuota` API is in `main`. The chart installs its CRD with `apps.enabled=true`.
- The claim check container can refuse a tenant from a limits file. See "The refusals of the claim check".
- With `apps.enabled`, the TenantQuota controller makes the gateway customer of each tenant label and writes the use to the status. See "The gateway customer" and "The controller".
- With a tenant claim, the tenant token binds each model call of an App pod to the tenant of a request. See "The tenant token".
- With a tenant claim (`apps.claimCheck.tenantClaim`), each AppVersion pod gets the limits file of its tenants. Then the claim check refuses a tenant above its rate, a tenant at its quota and, if the chart says so, an unknown tenant. See "The limits file" and "A tenant with no TenantQuota".
- `koodam usage --by tenant` shows the use of each tenant of a namespace. See "The spend report".

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
| `tenant` | Necessary. The tenant label of the signed claim, with 1 to 253 characters and a maximum of 253 bytes in UTF-8. You cannot change it after create. The API refuses the reserved label `koodam-no-tenant`. |
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
| `observedTime` | The time when the controller last wrote a change of the status. |

## The refusals of the claim check

The claim check container `koodam-claim` reads the limits of the tenants from a file, and it reads the file again each 10 seconds. A file that is not valid keeps the last valid limits. The container does not start with a file that is not valid.

The check examines a request after it verified the signed claim:

| Reason | Status | Condition |
|---|---|---|
| `UnknownTenant` | `403` | The file does not name the tenant label, and the file refuses an unknown tenant. |
| `TenantQuotaExceeded` | `429` | The file marks the tenant at its limit of the current period. |
| `TenantRateLimited` | `429` | The token bucket of the tenant is empty. The header `Retry-After` gives the seconds until the next token. |
| `ReservedTenant` | `403` | The claim has the reserved label `koodam-no-tenant`. The check refuses it also with no limits file. |

- The answer has the reason in the header `Koodam-Reason` and in the body. The app container does not get a refused request.
- The ingress span of the request has the reason as its `outcome`, with the caller label and the tenant label ([app-ingress-spans.md](app-ingress-spans.md)).
- Each pod has its own token bucket for each tenant. A refused request uses no token, and the limit of one tenant has no effect on a different tenant.
- A request with no tenant label has no tenant limit. This occurs only if the claim check has no tenant claim.

## The limits file

With `apps.claimCheck.tenantClaim`, the controller writes one ConfigMap for each AppVersion, with the name `kd-<version>-tenants`. The check container of each pod of the version mounts it as its limits file.

- The file has each `TenantQuota` of the namespace of the version that applies to its App: a `TenantQuota` with no `appRef`, or with the `appRef` of the App. A tenant label of a different App or a different namespace is an unknown tenant for the version.
- Each pod gets the rate divided by the number of pods that share it. The burst of a pod is the burst divided by this number, to the next full number, with a minimum of 1. Thus the sum of the bursts can be more than the burst of the tenant.
  - The number is the sum of `spec.replicas` of the version and of each other version of the App with a weight above 0 in the spec of an endpoint. A caller can select each such version with the header `Koodam-App-Version`. Thus the rate is for the App, and a tenant does not get it one time for each version.
  - A version with a complete revoke has no pods, so it is not in the sum.
  - If the replicas of the versions are not in proportion to their weights, a tenant gets less than its rate.
  - Each pod has its own token bucket, so a tenant can get one request from each pod at one time. This can be more than the burst of the `TenantQuota`. The rate over time stays correct. [#428](https://github.com/mohanraj00/koodam/issues/428) has the work for a burst that the pods share.
  - A `TenantQuota` with no `appRef` applies to each App of the namespace, and each App gives the tenant the full rate. For a rate of one App, set the `appRef`.
- The file has the mark of the condition `QuotaExceeded`. While the mark is set, the claim check refuses each request of the tenant with `TenantQuotaExceeded`. Only the status `True` sets the mark. The status `Unknown` (the reason `RequestCountCapped`) sets no mark.
- A change of a `TenantQuota`, of its mark, of the weights of the App or of `spec.replicas` of a version of the App changes the ConfigMap. The kubelet then updates the file in the pods, and the check container reads it again. This can take more than 1 minute. The pods do not start again.
  - The pods do not read a new file at the same time. A new pod starts with the new, smaller rate. A pod that runs keeps its old rate until it reads the new file. Thus, after a new version gets a weight or a version gets more replicas, a tenant can get more than its rate for this time. Only a change of the App or of a version starts this time, and a tenant cannot make such a change. [#428](https://github.com/mohanraj00/koodam/issues/428) has the work for a limit that the pods share. See "How exact the limits are".
- A ConfigMap has a maximum of 1 MiB. This is space for some thousand tenants of one App.
- The mark comes from the gateway count of the tenant, so the mark is only as correct as that count:
  - With the tenant token, the gateway counts a model call of an App pod only for the tenant of a token. An app can use the token of a tenant for a model call of a different request until the token ends. Thus an app can add to the count of a tenant that sent a request in that time, and to no other count.
  - With `apps.claimCheck.tenantToken=false`, the code in the pod selects the tenant label of each model call. An app can then fill the quota of a tenant with the calls of a different tenant, and the claim check refuses the requests of the first tenant.
  - The rate limit of the claim check does not use the gateway count. See "The tenant token" for the other limits.
- Without a tenant claim, the claim check has no tenant label, and a pod gets no limits file.

## A tenant with no TenantQuota

By default, the claim check accepts a tenant label with no `TenantQuota`, with no limit. The chart can change this:

```yaml
tenants:
  defaultQuota:
    rateLimit:
      requestsPerSecond: "5"
      burst: 10
  refuseUnknown: false
  usageInterval: 30s
```

- `tenants.defaultQuota.rateLimit` gives such a tenant a rate limit. Each tenant label has its own token bucket. The gateway has no customer for such a tenant, so it has no quota for each period.
- `tenants.refuseUnknown: true` refuses such a tenant with the status 403 and the reason `UnknownTenant`. Then `defaultQuota` has no effect.
- `tenants.usageInterval` is the time between 2 reads of the use from the gateway.
- `defaultQuota` and `refuseUnknown` need `apps.claimCheck.issuer` and `apps.claimCheck.tenantClaim`. The chart refuses them without these values.

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

## The controller

The TenantQuota controller runs with `apps.enabled`. For each `TenantQuota` it does these steps:

1. It adds the finalizer `core.koodam.dev/tenantquota-customer`.
2. It makes the gateway customer of the tenant label with the limits of the spec. After a change of the spec, it sets the limits again.
3. Each 30 seconds, it reads the spend and the requests of the current period from the gateway and writes them to the status. The manager flag `--tenant-usage-interval` changes this time. The controller writes the status only if a value changed.
4. It sets the condition `QuotaExceeded`. The status is `True` with the reason `TenantQuotaExceeded` when the requests or the spend are at the limit of the spec or above it. A new period of the gateway sets it back to `False` with the reason `WithinQuota`.
5. If the gateway stopped the count of the requests at its maximum, and this count is below `spec.requests`, the status is `Unknown` with the reason `RequestCountCapped`. The `requests` of the status are then a minimum. A spend at its limit still gives `True`.

- Each change of `QuotaExceeded` goes to the log of the manager, with the typed reason, the tenant label, the requests and the spend.
- The condition `Ready` is `False` with the reason `GatewayError` while a gateway call fails. The message is fixed text. `QuotaExceeded` then keeps its last value.
- If 2 or more `TenantQuota` objects of the install name one tenant label, only the oldest one has an effect. Each other one has `Ready=False` with the reason `DuplicateTenant`, and no use in its status. When the oldest one goes, the next one gets the label, and the customer keeps its spend.
- A `TenantQuota` with the reserved label `koodam-no-tenant` has no effect. The API refuses a new one. For an object that is older than this rule, the controller sets `Ready=False` with the reason `ReservedTenant`. It makes no gateway call for the object, and a delete of the object does not delete the gateway customer. See "The tenant token".
- A delete of the last `TenantQuota` of a tenant label deletes the gateway customer and its budget first, and then removes the finalizer. While the gateway does not answer, the `TenantQuota` stays.

## The spend report

```sh
koodam usage --by tenant -n shop
```

The command shows 1 line for each `TenantQuota` of the namespace that is `Ready`: the tenant label, the App, the requests and the spend of the current period, the mark `EXCEEDED` and the end of the period. It reads the status of the `TenantQuota` objects, so it shows only the tenants of that namespace. [cli.md](cli.md) has the columns.

The gateway counts a tenant label for the full install. Thus the use of a tenant has its model calls through each App of the install, also for a `TenantQuota` with an `appRef`.

## How exact the limits are

Koodam has no count that all the pods share before each request. A limit that comes from the state of one pod, or from a read at an interval, is thus not exact. A tenant can get more than a limit in these cases:

| Limit | Cause | Effect |
|---|---|---|
| `requests` | Koodam sends no limit of the request count to the gateway. The controller reads the count from the gateway at each `tenants.usageInterval`. Then the mark of the condition `QuotaExceeded` goes to the pods in the limits file. | The claim check accepts the requests of the tenant until each pod has the mark. With no `rateLimit` and no `spend`, nothing limits the count of these requests. |
| `rateLimit.requestsPerSecond` | The pods do not read a new limits file at the same time. | After a new version gets a weight or a version gets more replicas, a tenant can get more than its rate until each pod reads the new file. |
| `rateLimit.burst` | Each pod has its own token bucket. | A tenant can get one request from each pod at one time. |

- Set a `rateLimit` or a `spend` limit together with `requests`. The rate then limits the requests in the time before the mark. The `spend` limit does not use the mark: the gateway refuses a model call of a customer at its budget.
- The rate and the burst of `tenants.defaultQuota` have the same 2 causes as the rate and the burst of a `TenantQuota`.
- [#429](https://github.com/mohanraj00/koodam/issues/429) has the work for a request limit at one shared point. [#428](https://github.com/mohanraj00/koodam/issues/428) has the work for a rate and a burst that the pods share.

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
| `AppKeyOutOfPod` | The model call has the gateway key of an AppVersion, and the pod is not a pod of that AppVersion. |
| `KeyScanLimit` | The model call has more key texts than the proxy examines, or its headers, its query and its path are together too long, or a query value has an escape that is not valid. |

- An app can make a model call only for a request with a tenant label. A model call with no request of a tenant gets the refusal `TenantTokenMissing`.
- A tenant label must have only visible ASCII characters, with no space at its start or end.
- Each model call of an Agent pod or a Workspace pod gets the reserved customer `koodam-no-tenant`. Thus the code in these pods cannot make the gateway count a call for a tenant.
- No tenant can have the label `koodam-no-tenant`. The claim check refuses a claim with this label (`ReservedTenant`), the egress proxy refuses a token with it, the API refuses a `TenantQuota` with it, and the gateway client makes no customer limits for it. Limits on this customer would stop the model calls of each Agent and each Workspace.
- The proxy refuses the gateway key of an AppVersion in a model call of a different pod (`AppKeyOutOfPod`). An App pod can send its key to an Agent pod or a Workspace pod. Without this check, that pod can make a model call with the key of the App and with no tenant.
  - The proxy reads the hash of the key from the status of the AppVersion, and it makes the hash of each key text in the call.
  - It examines each header value, each query value and the path of the call. These include `Authorization`, `x-litellm-api-key`, `API-Key`, `x-api-key`, `x-goog-api-key`, `Ocp-Apim-Subscription-Key`, `litellm_user_api_key`, `Sec-WebSocket-Protocol` and the query value `key`.
  - It examines each value in the forms that the gateway reads: with no change, with a word (`Bearer`, `bearer` or `Basic`) removed at each position, and as base64 text. For base64 text, it uses the 2 behaviors that the Python decoder of a gateway can have for a pad character.
  - The scan of one call has 2 limits, so a call with much text cannot use much memory or time of the proxy. One limit is for the number of key texts. The other limit is for the total length of the headers, the query and the path. A call of a client has one short key. The proxy refuses a call over a limit (`KeyScanLimit`), because a key can be in the part that it did not examine. It also refuses a call with a query value that has an escape that is not valid.
- The gateway does not get the token, and no span has the token or the tenant label.

This is a change from v0.4.0 for an install with a tenant claim: the app must send the header `Koodam-Tenant-Token` with each model call. With `apps.claimCheck.tenantToken=false`, the install has the behavior of v0.4.0.

The tenant token has these limits:

- An app can use the token of a tenant for a model call of a different request, until the end time of the token. It cannot use the label of a tenant that sent no request to it in that time.
- A principal that can create pods or read Secrets in the namespace of the App can read the seed. It can then sign a token for each tenant label in that namespace.
- The proxy finds the pod of a call from the source IP and the pod labels. A principal that can change the labels of a pod can change the identity of the pod.
- The proxy must be on the path of each model call. A gateway of the mode `existing` at a public address, or an `extraEgress` rule that opens the gateway, lets a pod call the gateway directly. The gateway then trusts the label that the pod sends.
- The key check of the proxy is defense in depth. It is not a full bind of the key to the pod:
  - A pod that calls the gateway directly, as in the limit before this one, can use the key of an App.
  - The check finds a key in the locations and the forms that the gateway version of the chart reads. A different gateway version or a different gateway configuration can read a key from a different location.
  - An app can make a model call for a different pod. Such a call needs a tenant token, so the gateway counts it for a tenant that sent a request in the life of the token.
- After the loss of the Secret of the seed, the version gets a new key and new pods. The proxy refuses the model calls of the old pods with `TenantTokenInvalid` until they stop.
