# Operations page

Release: v0.6.0.

The operations page is a read-only web page of the [spend API](spend-api.md). It shows the runs, the detail of one run, the spend in a period by a label and by namespace, and the period budget of each namespace. It has no action that changes the cluster.

## Open the page

The `spend-api` server serves the page at `/`. To try it with no cluster, run the example. It uses a fake gateway and a fake Kubernetes API:

```sh
go run ./examples/operations-page
```

1. Open `http://127.0.0.1:8080/`.
2. Give the token `example-admin`. The page shows the runs and the spend of `team-a` and `team-b`.
3. Select "Forget the token", then give `example-team-a`. The page shows only `team-a`.

The example tokens are not secret. Do not use them on a server that reads a real cluster. On a real cluster, start `spend-api` as [spend-api.md](spend-api.md#start-the-server) tells, with `-viewer-review`. Then each person sees only the namespaces where that person can list Agents.

## Result

- The run list, the spend by label and the budget table read `/v1/runs`, `/v1/spend` and `/v1/budgets`.
- The detail of one run shows the Job, the pods, the status conditions and the last 200 log lines, as the server redacted them.
- A live watch shows a new phase with no reload. If the watch is not available, the page reads the run again at the `POLL_MS` interval of `internal/opspage/static/page.js`.
- Each table shows the items of the first `MAX_PAGES` pages. If the server has more items, the status line tells you.

## Errors

The status line above each table shows the error of the API call. The page uses the errors of the [spend API](spend-api.md#errors):

| Status | Meaning |
|---|---|
| 401 | The token is missing or not valid. Give the token again. |
| 403 | The viewer review refused the namespace. |
| 504 | An upstream read took more than `-request-timeout`. |

## Security

- The page keeps the token in memory only. A reload or a close of the page forgets it. The page does not write the token to storage, to the URL or to the console. The watch sends the token in the `Authorization` header.
- The page files and the mugham kit files are in the binary. The server sends a `Content-Security-Policy` that permits no inline script and no other origin. `internal/opspage/README.md` tells how to update the kit.
- The page sends GET requests to `/v1/` only.
