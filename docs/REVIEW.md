# Review rules

These rules apply to every PR to `main`. [AGENTS.md](../AGENTS.md) owns the general workflow and the 4 review gates.

## Cross-harness review

A harness does not review its own change. The branch prefix or the `agent:` label names the harness that wrote the change. The reviews run on GitHub. No harness reviews a PR by default, so the PR author asks in a PR comment.

| Builder | Code review comment | Security review comment |
|---|---|---|
| Claude Code | `@codex review` | `@codex security review` |
| Codex | `@claude review this PR. Use the Review guidelines in AGENTS.md.` | `@claude do a security review of this PR. Use the Review guidelines in AGENTS.md.` |
| agy | `@claude review this PR. Use the Review guidelines in AGENTS.md.` | `@claude do a security review of this PR. Use the Review guidelines in AGENTS.md.` |

Follow these rules for each request:

1. Ask when CI is green and the PR is ready.
2. For a Codex code review request, write `Head: <sha>` on the next line of the comment, with the short SHA of the PR head commit. Do not add this line to a Claude request. The Claude review starts only for the exact text in the table, and its comment names the head that it reviewed.
3. Write a security review request as the exact text in the table, with no other text in the comment. If the comment has other text, for example a `Head:` line, Codex does a code review and no security review. The Codex result names the commit that it reviewed.
4. Ask one time for each head commit. Before you ask, read the PR comments. If a request names the current head, or comes after the last push, do not ask again.
5. If you push fixes after a review, ask again for the new head. Ask at most 3 times for each PR. After that, ask the maintainer to review.
6. Fix each real finding, or write on the PR why it is not a defect.

For an outside PR, do not ask for a review until the maintainer adds the label `ready-for-review`. If the GitHub reviewer is not available, use the local fallback in AGENTS.md.

Codex reviews through the Codex GitHub integration. Claude Code reviews through the [Claude review](../.github/workflows/claude-review.yml) workflow. The workflow starts only if all of these are true:

- The comment is on a PR.
- The comment body is exactly one of the two Claude request texts in the table. GitHub compares these texts with no regard to letter case.
- The commenter is an owner, a member or a collaborator.

A comment or a review with other text does not start the workflow. The comment text selects one of two fixed prompts, and it never goes into a prompt.

### Claude review design

An issue comment event reads the workflow from `main`, so a PR cannot change this workflow for its own review. The workflow is a copy of the hardened form in `mohanraj00/.github`. It has two jobs.

The `review` job has `contents: read` and `pull-requests: read` only. It reads the base branch of the PR from the API. It uses the `claude-review` environment, which holds the `CLAUDE_CODE_OAUTH_TOKEN` secret. Only the CLI step gets the secret. The environment needs a deployment branch rule for `main`. The job does these steps:

1. It checks out `main` and fetches the PR commits as Git objects only. It never checks out PR files, so the PR cannot supply instructions, settings or code.
2. It writes the diff from the base branch of the PR to the PR head. If the diff is empty, the job fails.
3. It installs one exact version of the Claude Code CLI and checks the SHA-512 value of the package.
4. It runs the CLI in restricted mode with only the `Read`, `Grep` and `Glob` tools, no MCP server, no slash command and a turn limit. These tools cannot read `/proc`. The prompt says that the diff is untrusted data.
5. It checks the JSON result: `type` is `result`, `subtype` is `success`, `is_error` is false, `result` is not empty and `permission_denials` is empty. If a check fails, the job fails.
6. It keeps the review text and the head SHA in an artifact.

The `post` job has `contents: read`, `issues: write` and `pull-requests: write` only, with no secret and no environment. A comment on a PR needs `pull-requests: write`. It runs [tools/claude_review.py](../tools/claude_review.py) from the `main` checkout. The comment starts with a marker that holds the kind, the head SHA, the count of findings and the run ID. If the `review` job failed, the comment holds only `NO VALID CLAUDE REVIEW` and the head SHA.

The `post` job writes as the workflow bot. A mention in the bot comment can start a review, and the gates trust records from this bot. Thus the script treats the model text as untrusted. It escapes `&` and `<`, changes each `@` to a character that makes no mention, and limits the length.

The workflow does not stop a second request for the same head. Follow the request rules above: ask one time for each head commit, and at most 3 times for each PR.

`make check` runs the tests of the script and of the workflow design in `tests/test_claude_review.py`. CI runs the same tests.

### Use of a Claude result

The `cross-review` and `security-review` gates read the marker of a Claude result. They accept the marker only from an `issue_comment` run of this workflow on the default branch. A Claude code review passes `cross-review` only with `Findings: 0` for the current head.

The review has no inline comments and no conversation. A push makes a new head, so ask again as the request rules say. The OAuth token is a static secret. If you suspect a leak, revoke it and make a new one.
