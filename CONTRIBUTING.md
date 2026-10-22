# Contributing to Koodam

Thank you for your help. Read these rules before you write code. A PR that does not obey them gets a failed check and no review.

## 1. Open an issue first

1. Open an issue. Tell the problem and the change that you propose.
2. Wait for the maintainer to add the label `accepted`.
3. Do not start an issue with the label `decision`. The maintainer must decide first.

The PR body must have `Fixes #N` or `Part of #N` for an open issue with the label `accepted`. The PR Gate check examines this. If the maintainer adds the label after you open the PR, edit the PR body to run the check again.

## 2. Keep the PR small

Keep a PR to about 400 changed lines. These generated files do not count:

- `zz_generated.*`
- `config/crd/bases/` and `config/rbac/role.yaml`
- `charts/koodam/files/` and `charts/koodam/crds/`
- `internal/usage/prices/*.json`

If the change is larger, split it into PRs that each pass CI. Use `Part of #N` in each PR.

## 3. Test before you push

You need Go 1.26. Run these commands and make sure that they pass:

```sh
make test
make lint
```

For a change to the API types or the markers, generate the code again:

```sh
make manifests generate helm-sync
```

Do not edit the generated files by hand. [docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) has the layout.

The kind e2e job does not run on a PR from a fork until the maintainer adds the label `ok-to-test`. Until then, the job fails at once. After the maintainer adds the label, push a commit to run the job.

## 4. Dependencies

Use only dependencies with the licence Apache-2.0, MIT, BSD-2-Clause or BSD-3-Clause. Do not add a GPL, AGPL or LGPL dependency.

## 5. Credentials

Never put a credential (a key, a token, a password or a kubeconfig) in a test fixture, a log line, an error message or the PR text. If you find a security problem, do not open a public issue. Use the private vulnerability reporting of this repo.

## 6. Review

The maintainer reviews each PR. Changes to `api/`, `config/rbac/`, `internal/gateway/`, `charts/` and `.github/` need a review from the code owner. An agent review starts only after the maintainer adds the label `ready-for-review`.

A PR from a fork with no activity for 30 days is closed. You can reopen it when you continue the work.
