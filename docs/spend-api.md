# Spend API

The spend API joins Agent data from Kubernetes with spend from LiteLLM. Its public contract is under
`/v1`. Send GET or HEAD. Other methods get status 405. HEAD gives the same status and no body.
Send an `Authorization: Bearer` header on each GET or HEAD, including the OpenAPI request.

## Start the server

Build with `go build ./cmd/spend-api`. Run the binary with an identity that has the permissions of
[Server permissions](#server-permissions) in each namespace. The server uses the current kubeconfig or the pod's service
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

## Container image

Each release tag publishes the image `ghcr.io/mohanraj00/koodam-spend-api` for `linux/amd64` and
`linux/arm64`. A tag `vX.Y.Z` gives the image tags `X.Y.Z` and `X.Y`. The image runs `/spend-api` as
the user `65532` on a distroless base, and the release has the provenance and the SBOM of the image.
The binary listens on `127.0.0.1:8080` by default, so set `-listen :8080` in a pod.
Use the image digest, not the tag. To build the image locally, run `make docker-build-spend-api`.

The chart does not deploy spend-api. Run it in your own Deployment, with a service account that has the
permissions in this file. The image does not include the issuer JWKS file or the master key file.
Mount them from a Secret.

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

The server also needs the permissions of [Server permissions](#server-permissions) in each namespace of `-namespace`.

## Server permissions

The identity of the server needs only these read verbs in each namespace of `-namespace`:

| Verb | Resource | Reason |
|---|---|---|
| `list` | `agents.core.koodam.dev` | `GET /v1/runs` and `GET /v1/spend` |
| `get` | `agents.core.koodam.dev` | `GET /v1/runs/{namespace}/{name}` reads one Agent |
| `get` | `spendbudgets.core.koodam.dev` | `GET /v1/budgets` |
| `get` | `jobs.batch` | The run path reads the Job in `status.job` of the Agent |
| `list` | `pods` | The run path finds the pods of the Job by the label `batch.kubernetes.io/job-name`. A Job does not name its pods, so `get` is not sufficient. |
| `get` | `pods/log` | `GET /v1/runs/{namespace}/{name}/log` reads the log of the agent container |

The server reads the full pod objects, but a response has only the fields in the OpenAPI document. The
server does not need `watch`, and it never writes to the cluster.

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
and give a bearer token. The page shows the runs, the detail of one run, the spend in a period by a
label and by namespace, and the period budget of each namespace. It reads the API again at the `POLL_MS` interval in
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
check. After a change of `spec.period`, the old period of the status can overlap the current period.
The controller then keeps the old spend and rebuilds the reservations. Thus the item shows the values of
the status, not zero.

## Read one run

`GET /v1/runs/{namespace}/{name}` gives one Agent run. The namespace must be a namespace of
`-namespace`. In the viewer mode, the server does one SubjectAccessReview for `list` on
`agents.core.koodam.dev` in that namespace only. A namespace that the server does not serve gets
status 403, the same as a namespace that the caller cannot read. A missing Agent gets status 404. The
path has no query.

The response has these fields:

- `id`, `name`, `namespace`, `phase`, `startedAt` and `completedAt`: the same values as in `GET /v1/runs`.
- `conditions`: the status conditions of the Agent, for example `Provisioned` and `Revoked`. The
  server redacts each message, as [Redaction](#redaction) says.
- `job`: the run Job in `status.job`, with its counts, times and conditions. If the Agent has no Job,
  or if the Agent does not control the Job of that name, the response has no `job`.
- `pods`: the pods that the Job controls, newest first, with a maximum of 10. The server reads each
  page of the pods with the Job label. If more than 500 pods have the label, the response has status 502.
  Each pod has its phase, its start time and the state of each init container and container. A
  container state has the reason and the exit code, but no message, because the termination message comes from the code of the run.

## Read the log of a run

`GET /v1/runs/{namespace}/{name}/log?lines=200` gives the last lines of the first container of the
newest pod of the run. This container runs the agent. Set `lines` from 1 to 1000. The default is 200.
Only the viewer mode serves this path. In the other modes, the response has status 403, because the
server cannot check the access of the caller to the log. In the viewer mode, the server does two
SubjectAccessReviews in the namespace of the path: `list` on `agents.core.koodam.dev` and `get` on
`pods/log`. The caller needs both.

The limits of the log path are the constants in `internal/spendapi/log.go`:

- `maxLogLines`: the maximum of `lines`.
- `maxLogRead`: the most bytes that the server reads from the API server for one request.
- `maxLogBytes`: the most log bytes in a response, before the redaction. The server keeps the newest
  lines.
- `keyContext`: the count of the lines that the server reads before the tail, for the redaction.
- The request time limit of `-request-timeout` applies to the read of the log.

At each cut of a limit, the server drops the part of the line at the cut. Thus a response has no part
of a line. The server reads one more line than the tail and the `keyContext` lines, so `truncated`
is true only if the log has more lines than the response. If the log has more lines than the response, `truncated` is true. If the container did not
start, the response has status 409. If the run has no pod, the response has status 404.

The server redacts each line before the bytes leave the server, as [Redaction](#redaction) says. A
limit can cut a PEM private key, so the server redacts the `keyContext` lines before the tail too.
If the lines that the server reads have the END line of a key but not its BEGIN line, the server
redacts each line up to the END line. If the log can have lines before the lines that the server
reads, the server redacts the lines at the start that can be the body of a key.

## Redaction

The server redacts each log line and each condition message before the bytes leave the server. It
removes the terminal escape sequences and the control characters first. Then it replaces each match of
a fixed pattern list with `[redacted]`. The list is `redactions` in `internal/spendapi/redact.go`, and
each pattern has a test:

- PEM private keys, with each line up to the END line;
- JSON Web Tokens, for example Kubernetes service account tokens;
- keys that start with `sk-`, for example a gateway key;
- GitHub, AWS access key and Slack tokens;
- SHA-256 values in hex, for example a key hash;
- the credentials of an `Authorization` header (`Bearer`, `Basic` or `token`);
- the login and password of a URL;
- the value of a field with a credential name, for example `password=`, `"api_key":`, `GITHUB_TOKEN=`
  or `login:`. A quoted value includes each escaped quote. If a quoted value has no end quote, the
  server redacts the rest of the line.

The server redacts each line of a message with more than one line. If a PEM private key has no END
line, the server redacts each line after the BEGIN line.

The patterns can also redact text that is not a credential. A credential in a form that no pattern
matches stays in the response. Do not use the redaction as the only control for a credential in a log.

## Read more pages

Both lists accept `limit` from 1 to 100. The default is 100. The response has an `items` array.
If it has `nextCursor`, send that value as the `cursor` in the next request with the same filters.
Treat the cursor as opaque. If the API gives status 410, start again with no cursor.
The group list reads the current Agent labels on each page. A label change can move a run between
groups while a client reads the report.

Get the [OpenAPI document](spend-api.openapi.json) from `GET /v1/openapi.json`.
