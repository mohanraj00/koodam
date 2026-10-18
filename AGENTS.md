# Working on Koodam

Koodam is a Kubernetes operator (Go, Kubebuilder) and a CLI for agent runs. [README.md](README.md) has the goal and the plan. [docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) has the layout and the generated-code rules. This file covers how work is run. `CLAUDE.md` imports it.

## Work starts from an issue

- Pick up work with `gh issue list` and `gh issue view N`. If there is no issue, open one first.
- Use one branch for each issue, with the name `N-short-slug`. The PR body says `Fixes #N`.
- CI must pass before a PR is ready. The gates for a merge are in "Review gates".
- If you find something out of scope, open an issue for it. Do not make the PR wider.
- Labels: `api` changes the CRD, `e2e` touches the kind tests, `security` touches credentials or RBAC, plus `bug`, `documentation` and `enhancement`. Each version has a milestone.
- Before you start an issue, add the label `agent:claude` or `agent:codex`. Read the labels again. If both labels are on the issue, the label that was added first wins (`gh api repos/mohanraj00/koodam/issues/N/events`). If your label was second, remove it and pick a different issue. Do not take an issue that has an open PR.
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
- Run `make test` before each push. CI is the authority for lint.

## Review gates

A PR merges only when all 4 gates pass. The PR template lists them.

1. **CI.** All workflows pass.
2. **Cross review.** The other harness reviews the diff against `main`. A PR from Claude Code gets a review from Codex (`codex exec review --base main`). A PR from Codex gets a review from Claude Code (`claude -p "<review prompt>" --permission-mode plan`). If the reviewer has no quota, use Antigravity (`agy -p "<review prompt>" --mode plan --sandbox`). Post the result as a PR comment that starts with `Review (<harness> <version>):`.
3. **Security review, if necessary.** It is necessary if the PR has the label `security`, or changes one of these:
   - keys, tokens, Secrets, logins, SSH keys or host keys;
   - RBAC markers, Roles, ServiceAccounts or NetworkPolicy;
   - `internal/gateway/`, the proxies, `koodam tunnel` or `koodam ssh`;
   - log lines, events, status fields or errors that can hold a credential.

   The other harness does it. It checks credential leaks (logs, status, errors, fixtures, PR text), each RBAC verb and its reason, the revoke-first order, and input that reaches a shell, a URL or a file path. Post the result as a PR comment that starts with `Security review (<harness>):`.
4. **Findings closed.** Fix each finding, or write the reason to not fix it in the PR. A finding out of scope becomes a new issue.

Never pass `--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox` or `--dangerously-bypass-hook-trust` to a reviewer.

## Autofix

- If CI fails, read the log (`gh run view --log-failed`), fix the cause and push. After 3 tries on the same failure, stop and ask the maintainer.
- If a review finds a defect, fix it, push, and ask the same reviewer to check the fix. After 2 rounds, stop and ask the maintainer.
- Never make a test pass by removing the check that fails. Never skip or turn off lint.

## Merge

- When the 4 gates pass, merge with `gh pr merge N --squash --delete-branch`. GitHub auto-merge is not available on this repo, so the agent that owns the PR merges it.
- Merge in dependency order, and merge as soon as the gates pass. Small, early merges make the rebases of the other lanes small.
- After each merge, rebase each open lane on `main` and run its tests again. If generated files conflict, run `make manifests generate` again. Do not merge a generated file by hand.
- Do not merge two `api` PRs one after the other without a rebase and a CI run between them.
- After the merge, write a comment on the issue with the PR and the commit.

## Stop and ask the maintainer

- The issue needs a decision, or the scope is not clear.
- A change needs a new RBAC verb without a clear reason, or a dependency that is not Apache-2.0 or MIT.
- Two reviewers do not agree on a security finding.
- The step is outward: a tag, a release, a visibility change or a post. Only the maintainer does these.

## Rules that are easy to break

- **Never write a credential value** (a gateway key, a token, a login) to a log, an event, the status, a test fixture or an error message. Use the key alias.
- **Generated code.** Edit the `*_types.go` files and the markers, then run `make manifests generate`. Do not edit `zz_generated.*`, `config/crd/bases/` or `config/rbac/role.yaml` by hand.
- **The controller RBAC** is the main attack surface. Each new verb in a `+kubebuilder:rbac` marker needs a reason in the PR.
- **Revoke first.** On a Job end or an Agent delete, block the key before the controller deletes anything or reads the spend. A finalizer keeps the Agent until the revoke is done.
- **Licences.** Apache-2.0 or MIT dependencies only. No GPL or AGPL dependencies.
- **Claims.** Every number in the README or a doc must match a results file from a spike or a benchmark. Do not type numbers.
- **Releases.** Only the maintainer tags a release or changes the repo visibility.

## Evidence

The spikes that decide each version are in the private `os-initiatives` project. The stage 1 and stage 2 spike code is in `/Users/mohangraj/Reboot/public/jobkey-spike/`. Use it as the reference for the lifecycle, not as code to copy.

## Writing

Write docs, comments and commit messages in ASD-STE100 Simplified Technical English. The README uses the maintainer's voice: first person, short, numbers instead of adjectives, no em-dashes.
