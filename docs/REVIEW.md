# Review rules

These rules apply to every PR to `main`. [STANDARD.md](STANDARD.md) owns the review gates, the reviewer for each builder, the fallback, the request cap and the rules to close a finding. This file has the request texts and the design of the Claude review workflow.

## Cross-harness review

The branch prefix or the `agent:` label names the harness that wrote the change. The reviews run on GitHub. No harness reviews a PR by default, so the PR author asks in a PR comment.

| Review | Request comment |
|---|---|
| Codex code review | `@codex review` |
| Codex security review | `@codex security review` |
| Claude code review | `@claude review this PR. Use the Review guidelines in AGENTS.md.` |
| Claude security review | `@claude do a security review of this PR. Use the Review guidelines in AGENTS.md.` |

[STANDARD.md](STANDARD.md) says which review each builder asks for.

Follow these rules for each request:

1. Ask when CI is green and the PR is ready.
2. For a Codex code review request, write `Head: <sha>` on the next line of the comment, with the short SHA of the PR head commit. Do not add this line to a Claude request. The Claude review starts only for the exact text in the table, and its comment names the head that it reviewed.
3. Write a security review request as the exact text in the table, with no other text in the comment. If the comment has other text, for example a `Head:` line, Codex does a code review and no security review. The Codex result names the commit that it reviewed.
4. Before you ask, read the PR comments. If a request names the current head, or comes after the last push, do not ask again.
5. If you push fixes after a review, ask again for the new head.

[STANDARD.md](STANDARD.md) has the request cap and the rules to close a finding.

[STANDARD.md](STANDARD.md) has the rules for a fork PR and for the fallback. The `gate` job of the Claude workflow stops a Claude review of a fork PR without the stage 2 owner approval of the current head and base. [AGENTS.md](../AGENTS.md) has the command for a local fallback review.

Codex reviews through the Codex GitHub integration. Claude Code reviews through the [Claude review](../.github/workflows/claude-review.yml) workflow. The workflow starts only if all of these are true:

- The comment is on a PR.
- The comment body is exactly one of the two Claude request texts in the table. GitHub compares these texts with no regard to letter case.
- The commenter is an owner, a member or a collaborator.

A comment or a review with other text does not start the workflow. The comment text selects one of two fixed prompts, and it never goes into a prompt.

### Claude review design

An issue comment event reads the workflow from `main`, so a PR cannot change this workflow for its own review. The workflow is a copy of the hardened form in `mohanraj00/.github`. It has three jobs.

The `gate` job has `actions: read`, `contents: read`, `issues: read` and `pull-requests: read` only, with no secret and no environment. It runs the `claude-review` check of the gates action, at the same SHA as the gates. It stops the request in these conditions:

- The PR is a fork PR, and it has no stage 2 owner approval of the current head and base.
- A trusted marker of the same kind names the current head and merge base.
- The PR has 3 trusted markers of the same kind.

If the gate passes, it gives the head, the base and the merge base to the `review` job. The requests on one PR run one at a time. A new pending request replaces an older pending request, and the older request posts no comment.

The `review` job has `contents: read` and `pull-requests: read` only. It reads the base branch of the PR from the API. It uses the `claude-review` environment, which holds the `CLAUDE_CODE_OAUTH_TOKEN` secret. Only the CLI step gets the secret. The environment needs a deployment branch rule for `main`. The job does these steps:

1. It checks out `main` and fetches the PR commits as Git objects only. It never checks out PR files, so the PR cannot supply instructions, settings or code.
2. It writes the diff from the merge base of the base branch and the PR head. If there is not exactly one merge base, or the diff is empty, the job fails. The job also fails if the head, the base or the merge base is not the one that the `gate` job approved.
3. It installs one exact version of the Claude Code CLI and checks the SHA-512 value of the package.
4. It runs the CLI in restricted mode with only the `Read`, `Grep` and `Glob` tools, no MCP server, no slash command and a turn limit. These tools cannot read `/proc`. The prompt says that the diff is untrusted data.
5. It checks the JSON result: `type` is `result`, `subtype` is `success`, `is_error` is false, `result` is not empty and `permission_denials` is empty. If a check fails, the job fails.
6. It keeps the review text, the head SHA and the merge base in an artifact.

The `post` job has `contents: read`, `issues: write` and `pull-requests: write` only, with no secret and no environment. A comment on a PR needs `pull-requests: write`. It runs [tools/claude_review.py](../tools/claude_review.py) from the `main` checkout. The comment starts with a `v2` marker that holds the kind, the head SHA, the merge base of the diff, the count of findings and the run ID. If the `review` job failed, the comment holds only `NO VALID CLAUDE REVIEW` and the head SHA. If the `gate` job stopped the request, the comment starts with `The Claude review did not start.` and gives the reason. This comment has no marker.

The `post` job writes as the workflow bot. A mention in the bot comment can start a review, and the gates trust records from this bot. Thus the script treats the model text as untrusted. It escapes `&` and `<`, changes each `@` to a character that makes no mention, and limits the length.

The `gate` job stops a second request of one kind for the same head and merge base, and a request after 3 of one kind. Still follow the request rules above and the request cap in [STANDARD.md](STANDARD.md).

`make check` runs the tests of the script and of the workflow design in `tests/test_claude_review.py`. CI runs the same tests.

### Use of a Claude result

The `cross-review` and `security-review` gates read the marker of a Claude result. They accept the marker only from an `issue_comment` run of this workflow on the default branch. A Claude code review passes `cross-review` only with `Findings: 0` for the current head and the current merge base. A new commit on the base branch does not change the merge base, so the review stays valid. If the PR gets a different base branch, the review stops, and you must ask again. The gates do not accept a `v1` marker.

The `security-review` gate writes a line about a clean Claude security review, but it does not pass on it. [STANDARD.md](STANDARD.md) has the approval rule for a Claude security result. If the review text does not agree with the count on the first line, the marker gives `findings=unknown`, and the gates do not accept it. Examples are a later line with a severity tag or a `path:line` reference, a second count that is different, and the text `INCOMPLETE REVIEW`.

The review has no inline comments and no conversation. A push makes a new head, so ask again as the request rules say. The OAuth token is a static secret. If you suspect a leak, revoke it and make a new one.
