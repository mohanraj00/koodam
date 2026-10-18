# Working on Koodam

Koodam is a Kubernetes operator (Go, Kubebuilder) and a CLI for agent runs. [README.md](README.md) has the goal and the plan. [docs/kubebuilder-guide.md](docs/kubebuilder-guide.md) has the layout and the generated-code rules. This file covers how work is run. `CLAUDE.md` imports it.

## Work starts from an issue

- Pick up work with `gh issue list` and `gh issue view N`. If there is no issue, open one first.
- Use one branch for each issue, with the name `N-short-slug`. The PR body says `Fixes #N`.
- CI must pass before a PR is ready.
- If you find something out of scope, open an issue for it. Do not make the PR wider.
- Labels: `api` changes the CRD, `e2e` touches the kind tests, `security` touches credentials or RBAC, plus `bug`, `documentation` and `enhancement`. Each version has a milestone.
- Before you start an issue, add the label `agent:claude` or `agent:codex`. Do not take an issue that has the other label or an open PR.

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
