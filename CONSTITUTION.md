# Koodam constitution

Version 1, 2026-11-09.

## Purpose

Koodam is where your agents live on Kubernetes. It gives Kubernetes-aware developers and teams a first-class developer experience for agent work, built from Kubernetes constructs.

## Boundaries

Agents, Workspaces and Profiles are Kubernetes resources. Thus a platform team controls them with the tools it already uses: RBAC, NetworkPolicy, LimitRange, admission policy and GitOps.

## Principles

- **Never write a credential value** (a gateway key, a token, a login) to a log, an event, the status, a test fixture or an error message. Use the key alias.
- **The controller RBAC** is the main attack surface. Each new verb in a `+kubebuilder:rbac` marker needs a reason in the PR.
- **Revoke first.** On a Job end or an Agent delete, block the key before the controller deletes anything or reads the spend. A finalizer keeps the Agent until the revoke is done.

## Evidence

- **Claims.** Every number in the README or a doc must match a results file from a spike or a benchmark. Do not type numbers.
- Each version starts with a spike that decides go or no-go before the build. The spike records are the evidence for every claim in the README.
- The spikes that decide each version are in a private repository of the maintainer. The stage 1 and stage 2 spike code is the reference for the lifecycle. Use it as a reference, not as code to copy. If you need the spike code, ask the maintainer for its location.

## Contributions

- Do not write a local path or a private repository name in this repository.
- **Licences.** Apache-2.0, MIT, BSD-2-Clause or BSD-3-Clause dependencies only. No GPL, AGPL or LGPL dependencies.
- [CONTRIBUTING.md](CONTRIBUTING.md) has the rules for a contributor. The project license is Apache-2.0 ([LICENSE](LICENSE)).

## Governance

<!-- governance:start v1 -->
This project follows the project standard in `docs/STANDARD.md`. That file is an exact copy from `mohanraj00/.github`. The standard owns the rules for builds, reviews, approvals and CI. A project exception to the standard is valid only when the standard records it.

The documents own these topics:

- `CONSTITUTION.md`: the purpose, the boundaries, the principles, the evidence rules, the contribution rules and the version log.
- `docs/STANDARD.md`: the builds, the reviews, the approvals and CI.
- `AGENTS.md`: the layout, the check command and the review guidelines.
- `CLAUDE.md` and `GEMINI.md`: imports only.

These rules apply to each change:

1. A harness does not review its own change. The Codex security review is the one exception, as the standard says.
2. The agents use the owner login. An agent never approves a PR and never merges a level 1 PR. An agent never adds the labels `accepted`, `security-reviewed`, `cross-reviewed` or `ready-for-review`.
3. The owner merges each level 1 PR.
4. Each tag, release, package publication, visibility change and outreach step needs a separate owner approval.
<!-- governance:end v1 -->

## Amendment

Change this constitution when the owner changes direction or evidence changes a decision. A change is a level 1 PR. Record the reason in the change, and add an entry to the version log.

Change the governance block only with a new version of the template in `mohanraj00/.github`.

## Version log

### Version 1, 2026-11-09

Make the constitution from template v1. Move the purpose, the boundaries, the principles, the evidence rules and the contribution rules from `README.md` and `AGENTS.md`. Reason: project standard version 6 requires a constitution with the governance block.
