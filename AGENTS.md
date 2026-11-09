# Working on Koodam

Koodam is a Kubernetes operator (Go, Kubebuilder) and a CLI for agent runs. [CONSTITUTION.md](CONSTITUTION.md) has the purpose, the boundaries, the principles, the evidence rules and the contribution rules. [docs/STANDARD.md](docs/STANDARD.md) has the rules for builds, reviews, approvals and CI. [README.md](README.md) has the plan. [docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) has the layout and the generated-code rules. This file covers the project steps of the work and the review guidelines. `CLAUDE.md` imports it.

## Work starts from an issue

- Pick up work with `gh issue list` and `gh issue view N`. [docs/STANDARD.md](docs/STANDARD.md) has the rules for the issue, the `agent:` label and the branch name.
- Use one branch for each issue. The PR body says `Fixes #N`.
- If you find something out of scope, open an issue for it. Do not make the PR wider.
- Labels: `api` changes the CRD, `e2e` touches the kind tests, `security` touches credentials or RBAC, plus `bug`, `documentation` and `enhancement`. Each version has a milestone.
- After you add the `agent:` label, read the labels again. To find the label that was added first, use `gh api repos/mohanraj00/koodam/issues/N/events`. If your label was second, remove it and pick a different issue. Do not take an issue that has an open PR.
- Do not implement an issue with the label `decision` until the maintainer answers. You can write a design proposal as a comment.

## Parallel lanes

Work in parallel when the issues allow it. If you only do one issue, these rules still apply to your branch.

- A lane is one issue, one git worktree, one branch and one agent. Run at most 3 lanes at the same time.
- Plan the lanes from the dependencies between the issues. Start the issues that block other issues first.
- Give each lane a file scope. Two lanes must not change the same files at the same time.
- Only one lane at a time changes the shared files:
  - `api/v1alpha1/*_types.go` and the generated files (`zz_generated.*`, `config/crd/bases/`, `config/rbac/role.yaml`);
  - `go.mod` and `go.sum`;
  - `AGENTS.md`, `Makefile` and `.github/`.
- Keep each PR small: about 400 changed lines or fewer, not counting generated files. Split a larger issue into PRs that each pass CI.
- Run `make check` before each push. It runs the Go tests and the Python tests of `tools/`. CI is the authority for lint.

## Review gates

[docs/STANDARD.md](docs/STANDARD.md) has the review gates, the reviewer for each builder, the fallback, the request cap and the rules to close a finding. [docs/REVIEW.md](docs/REVIEW.md) has the request texts. The project steps are below.

- For a local fallback review with Antigravity, run `agy -p "<review prompt>" --mode plan --sandbox`. Post the result as a PR comment that starts with `Review (<harness> <version>):`.
- Post a local security result as a PR comment that starts with `Security review (<harness>):`.

### Outside PRs

An outside PR comes from a fork, by a person who is not the maintainer. [CONTRIBUTING.md](CONTRIBUTING.md) has the rules for the contributor. [docs/STANDARD.md](docs/STANDARD.md) has the rules for the reviews and the approval of a fork PR.

- Never run the code of a contributor with credentials, a gateway key, a token or a cluster kubeconfig. Read the diff. Do not execute it.
- Do not add the label `ok-to-test`. Only the maintainer adds it. The `## Governance` section of [CONSTITUTION.md](CONSTITUTION.md) names the other labels that an agent never adds.
- The labels `needs-issue` and `too-large` are markers for the maintainer. The PR Gate does not read them.

Never pass `--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox` or `--dangerously-bypass-hook-trust` to a reviewer.

## Review guidelines

Flag these as high priority in a PR review:

- A credential value (a gateway key, a token, a login) in a log, an event, the status, a test fixture, an error or the PR text.
- A new verb in a `+kubebuilder:rbac` marker with no reason, or a wider Role, ServiceAccount or NetworkPolicy.
- A break of the revoke-first order: the controller deletes a resource or reads the spend before it blocks the key.
- Input from a user or an Agent spec that reaches a shell, a URL or a file path with no check.
- A hand edit of a generated file.
- A dependency with a licence that is not Apache-2.0, MIT, BSD-2-Clause or BSD-3-Clause.
- A number in the README or a doc with no results file.
- A removed or weaker test check, or lint that is turned off.
- A local path, a user name or a private repository name in a file, a commit message or the PR text.
- A workflow change that adds a trigger, a permission or a secret, or removes a SHA pin.

Do not review these paths: `zz_generated.*`, `config/crd/bases/`, `config/rbac/role.yaml`, `charts/koodam/files/`, `charts/koodam/crds/`, `internal/usage/prices/*.json` and `docs/results/`. Review the source of a generated file instead.

Report at most 3 nits. A nit is a style or naming issue with no effect on behavior. Put the nits after the defects.

A security review checks credential leaks (logs, status, errors, fixtures, PR text), each RBAC verb and its reason, the revoke-first order, and input that reaches a shell, a URL or a file path.

End a code review with one line. If the PR has the label `security`, or changes one of these, write `Security review: needed`, with the reason:

- keys, tokens, Secrets, logins, SSH keys or host keys;
- RBAC markers, Roles, ServiceAccounts or NetworkPolicy;
- `internal/gateway/`, the proxies, `koodam tunnel` or `koodam ssh`;
- log lines, events, status fields or errors that can hold a credential.

If not, write `Security review: not needed`. A security review does not end with this line.

## Autofix

- If CI fails, read the log (`gh run view --log-failed`), fix the cause and push. After 3 tries on the same failure, stop and ask the maintainer.
- If a review finds a defect, fix it and push. [docs/STANDARD.md](docs/STANDARD.md) has the request cap for the next review.
- Never make a test pass by removing the check that fails. Never skip or turn off lint.

## Merge

- [docs/STANDARD.md](docs/STANDARD.md) has the approval levels and who merges. When the author agent merges a level 0 PR, it uses `gh pr merge N --squash --delete-branch`.
- Merge in dependency order, and merge as soon as the gates pass. Small, early merges make the rebases of the other lanes small.
- After each merge, rebase each open lane on `main` and run its tests again. If generated files conflict, run `make manifests generate` again. Do not merge a generated file by hand.
- Do not merge two `api` PRs one after the other without a rebase and a CI run between them.
- After the merge, write a comment on the issue with the PR and the commit.

## Stop and ask the maintainer

- The issue needs a decision, or the scope is not clear.
- A change needs a new RBAC verb without a clear reason, or a dependency that is not Apache-2.0, MIT, BSD-2-Clause or BSD-3-Clause.

[docs/STANDARD.md](docs/STANDARD.md) has the rules for a reviewer disagreement. The `## Governance` section of [CONSTITUTION.md](CONSTITUTION.md) has the outward steps that need the owner.

## Rules that are easy to break

[CONSTITUTION.md](CONSTITUTION.md) has the credential rule, the controller RBAC rule, the revoke-first order, the claim rule and the licence rule.

- **Generated code.** Edit the `*_types.go` files and the markers, then run `make manifests generate`. Do not edit `zz_generated.*`, `config/crd/bases/` or `config/rbac/role.yaml` by hand.

## Writing

Write docs, comments and commit messages in ASD-STE100 Simplified Technical English. The README uses the maintainer's voice: first person, short, numbers instead of adjectives, no em-dashes.
