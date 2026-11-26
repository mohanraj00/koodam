# Operations page example

This example runs the spend API and the operations page on one machine. It needs no cluster and no
gateway. The program uses the same handlers as the `spend-api` binary:

- A fake gateway gives a synthetic spend for each run.
- A fake Kubernetes API gives 3 synthetic Agents in the namespaces `team-a` and `team-b`.
- The fake Kubernetes API also answers the TokenReview and the SubjectAccessReview of the viewer mode.
  It accepts only the two example tokens below.

## Run the example

1. From the repository root, run `go run ./examples/operations-page`.
2. Open `http://127.0.0.1:8080/` in a browser. To use a different address, add `-listen <host:port>`.
3. Give the token `example-admin`. The page shows the runs and the spend of the two namespaces.
4. Select "Forget the token". Then give the token `example-team-a`. The page shows only the runs and the
   spend of `team-a`.

The example tokens are not secret. Do not use them on a server that reads a real cluster.

## The check

`main_test.go` runs in `make check`, so CI runs it. The test does these steps:

1. It starts the example on a local port.
2. It reads the page, `page.js`, `page.css` and each kit file. It compares the SHA-256 value of each
   kit file with `internal/opspage/mugham.pin.json`, and checks the page policy header.
3. It reads `/v1/runs` and `/v1/spend` with each example token, and checks the namespaces and the spend.
4. It checks that no response header or body has a key value, a key hash, the master key or a token.
