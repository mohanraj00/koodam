# Spend API

The spend API joins Agent data from Kubernetes with spend from LiteLLM. Its public contract is under
`/v1`. Send GET or HEAD. Other methods get status 405. HEAD gives the same status and no body.
Send an `Authorization: Bearer` header on each GET or HEAD, including the OpenAPI request.

## Start the server

Build with `go build ./cmd/spend-api`. Run the binary with an identity that has `list` on
`agents.core.koodam.dev` in each namespace. The server uses the current kubeconfig or the pod's service
account. Set `-namespace` to select the namespaces, separated by commas, for example `team-a,team-b`.
The limit is 64 namespaces. The default is the namespace of the Kubernetes client. Each caller with a
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

For a local run, use `-static-token-file` with one token per line in place of the issuer flags.
The default listen address is `127.0.0.1:8080`. Set `-listen` for a different address.
Use HTTPS at the ingress when clients connect over a network.
Use `-request-timeout` to set the time limit for the Kubernetes and gateway reads of each request.
An expired request gets status 504. If an upstream read fails, the API gives an error with no partial
spend data.

## List runs

`GET /v1/runs` gives the retained Agent runs, one namespace after the other, in the order of
`-namespace`. Each item has the run ID, name, namespace, creation time
and declared budget. The API reads the lifetime spend of the key from LiteLLM. If LiteLLM no longer
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

For `koodam.dev/cost-group`, the API uses the group that the controller saved when it made the key.
For other label names, such as `team`, it uses the current Agent label. An Agent with no group
has an empty `labelValue`. Groups are in label value order.

Koodam has no declared period budgets. The period items omit `budget` and `remainingBudget`.
The run budgets apply to individual runs.

## Read more pages

Both lists accept `limit` from 1 to 100. The default is 100. The response has an `items` array.
If it has `nextCursor`, send that value as the `cursor` in the next request with the same filters.
Treat the cursor as opaque. If the API gives status 410, start again with no cursor.
The group list reads the current Agent labels on each page. A label change can move a run between
groups while a client reads the report.

Get the [OpenAPI document](spend-api.openapi.json) from `GET /v1/openapi.json`.
