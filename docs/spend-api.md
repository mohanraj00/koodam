# Spend API

The spend API joins Agent data from Kubernetes with spend from LiteLLM. Its public contract is under
`/v1`. Send GET or HEAD. Other methods get status 405. HEAD gives the same status and no body.
Send an `Authorization: Bearer` header on each GET or HEAD, including the OpenAPI request.

## Start the server

Build with `go build ./cmd/spend-api`. Run the binary with an identity that has `list` on
`agents.core.koodam.dev` and `get` on `spendbudgets.core.koodam.dev` in each namespace. The server uses the current kubeconfig or the pod's service
account. Set `-namespace` to select the namespaces, separated by commas, for example `team-a,team-b`.
The server refuses a list that is longer than the `maxNamespaces` constant in
`internal/spendapi/api.go`. The default is the namespace of the Kubernetes client. Each caller with a
valid claim can read all the namespaces that the server uses.

```sh
./spend-api -namespace dev-sample -gateway-url https://gateway.example.com \
  -master-key-file /run/spend-api/master-key -issuer https://issuer.example.com \
  -audience spend-api -jwks-file /run/spend-api/jwks.json
```

Give the server the issuer's public JWKS file. Restart the server to load a new file. The server
verifies RS256 or ES256 signatures and checks the issuer, audience and time claims.
The `-caller-claim` flag defaults to `sub`. Set `-tenant-claim` to require a tenant claim.
These claims identify the caller. All permitted callers have the same access to this server.
A missing or invalid token gets status 401.

## Viewer review

For people, use `-viewer-review` in place of the issuer flags and the local token file. The caller
sends a Kubernetes token, for example from `kubectl create token` or an OIDC ID token that the API
server accepts. For each request, the server does these steps:

1. It sends a TokenReview to the API server. If the API server does not accept the token, the
   response has status 401.
2. It sends one SubjectAccessReview for `list` on `agents.core.koodam.dev` in each namespace of
   `-namespace`, with the user, the groups, the UID and the extra values of the token.
3. It reads only the namespaces where the review permits the caller. If there is no such namespace,
   the response has status 403. If a review fails, the response has status 502.

The server never impersonates the caller. It reads the Agents with its own identity, and it never
returns or logs the token. Set `-viewer-audience` to require a token audience. The API server must
then accept the token for that audience. The server does no cache of the reviews, so a change of the
RBAC of a caller applies to the next request.

The identity of the server needs `create` on `tokenreviews` and `subjectaccessreviews`. The built-in
ClusterRole `system:auth-delegator` has the same rules. This manifest gives the rules to the service
account `spend-api` in the namespace `koodam-system`. Change the names for your cluster.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: koodam-spend-api-viewer-review
rules:
  - apiGroups: ["authentication.k8s.io"]
    resources: ["tokenreviews"]
    verbs: ["create"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: koodam-spend-api-viewer-review
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: koodam-spend-api-viewer-review
subjects:
  - kind: ServiceAccount
    name: spend-api
    namespace: koodam-system
```

The server also needs `list` on `agents.core.koodam.dev` in each namespace of `-namespace`.

## Local tokens and limits

For a local run, use `-static-token-file` with one token per line in place of the issuer flags.
Give one of `-jwks-file`, `-static-token-file` or `-viewer-review`.
The default listen address is `127.0.0.1:8080`. Set `-listen` for a different address.
Use HTTPS at the ingress when clients connect over a network.
Use `-request-timeout` to set the time limit for the Kubernetes and gateway reads of each request.
An expired request gets status 504. If an upstream read fails, the API gives an error with no partial
spend data.

## Operations page

The server also serves a read-only operations page at `/`. Open the address of the server in a browser
and give a bearer token. The page shows the runs, the detail of one run, and the spend in a period by a
label and by namespace. It reads the API again at the `POLL_MS` interval in
`internal/opspage/static/page.js`. Use the viewer review mode, so that each
person sees only the namespaces where that person can list Agents.

- The page keeps the token in memory only. A reload or a close of the page forgets it. The page does not
  write the token to storage, to the URL or to the console.
- The page and the mugham kit files are in the binary. The server sends a `Content-Security-Policy`
  that permits no inline script and no other origin. `internal/opspage/README.md` tells how to update
  the kit.
- The page sends GET requests to `/v1/` only. It has no action that changes the cluster.
- Each table shows the items of the first `MAX_PAGES` pages in `page.js`. If the server has more items,
  the status tells you.

## List runs

`GET /v1/runs` gives the retained Agent runs, one namespace after the other, in the order of
`-namespace`. Each item has the run ID, name, namespace, creation time
and declared budget. It also has the phase, the models, and the start and end times of the run Job,
when the Agent has them. The API reads the lifetime spend of the key from LiteLLM. If LiteLLM no longer
has the key, the API uses the spend saved in the Agent status, if available. An Agent with no key
and no saved usage has zero spend. If the key is missing and the spend is unknown, the API omits
`spend` and `remainingBudget`.

Amounts are numbers in USD. The remaining budget is the budget minus the spend, with a minimum
of zero. The gateway can take time to record new spend.

## List spend by label

```text
GET /v1/spend?from=2026-01-01&to=2026-01-31&groupBy=label:koodam.dev/cost-group
```

Give both UTC dates in `YYYY-MM-DD` format. The range includes both dates.
The API sums daily spend from LiteLLM for the keys of retained Agents that existed before the
end of the range. A group includes the Agents of all the namespaces of the caller. A group can have zero spend. Deleted Agent objects are outside the report.
The report uses the dates of gateway calls. The gateway can remove older spend logs.
The API uses the open-source LiteLLM spend endpoint and groups the results in Koodam.

Use `groupBy=namespace` to group by the Agent namespace. Then `labelValue` holds the namespace.

For `koodam.dev/cost-group`, the API uses the group that the controller saved when it made the key.
For other label names, such as `team`, it uses the current Agent label. An Agent with no group
has an empty `labelValue`. Groups are in label value order.

The period items omit `budget` and `remainingBudget`, because a range of dates is not always a budget
period. Use `GET /v1/budgets` for the period budgets. The run budgets apply to individual runs.

## List budgets

`GET /v1/budgets` gives the [SpendBudget](spend-budgets.md) ledger of each namespace of the caller that
has one, in the order of `-namespace`. The path has no query and no pages. A namespace with no
SpendBudget has no item. Each item has these fields:

- `namespace`, `period` (`Day` or `Month`), `periodStart` and `periodEnd`: the current UTC period.
- `limit`, `spend`, `reserved` and `remaining`, in USD. `spend` is the settled spend of the Agent runs
  that started in the period and ended. `reserved` is the sum of the budgets of the active runs.
  `remaining` is the limit minus the spend and the reserved budgets, with a minimum of zero.
- `exceeded`: true if the last run that the controller checked had to wait.
- `waitingRuns`: the count of the runs that waited at the last check.

If the controller did not move the ledger to the current period yet, the item shows the current period
with no spend and no reservation. The controller drops the reservations of the old period at the next
check.

## Read more pages

Both lists accept `limit` from 1 to 100. The default is 100. The response has an `items` array.
If it has `nextCursor`, send that value as the `cursor` in the next request with the same filters.
Treat the cursor as opaque. If the API gives status 410, start again with no cursor.
The group list reads the current Agent labels on each page. A label change can move a run between
groups while a client reads the report.

Get the [OpenAPI document](spend-api.openapi.json) from `GET /v1/openapi.json`.
