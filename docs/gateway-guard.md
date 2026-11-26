# The gateway guard for instructions in untrusted content

An agent reads text from documents, web pages and tools. This text can hold instructions for the model. The gateway guard is an optional hook of the bundled gateway. It examines this text in each model request and records, marks or blocks a match. This page is the design and the log line contract (v1) for the programs that read the decisions, for example Nookku.

> Status: [#248](https://github.com/mohanraj00/koodam/issues/248). The hook and the chart values are in the chart. The [image check](#check-the-pinned-gateway-image) and the kind test prove the hook in the pinned gateway image. The guard is off by default.

**The guard is a tripwire for known forms. It is not a defense.** It finds only the fixed forms that its rules name. A different text can always go past it. The [egress policy](traced-egress.md) stays the boundary that limits the effect of a successful injection: a pod that follows an instruction still cannot call a host that its policy refuses.

## Where the guard runs

The guard is a hook in the bundled LiteLLM gateway, in the same pattern as the [zone route hook](zones.md). The hook reads each model request before the gateway sends it to the provider.

- The guard covers each object that uses a gateway key: an Agent run, a Workspace, a TestKey and an App.
- The identity of a decision comes from the key metadata of the gateway, not from the request. The pod cannot change it.
- The egress proxy does not read the body of a model request, and the guard does not change this. The proxy stays small, and its [span contract](egress-spans.md) does not change.

The hook goes in the `litellm_settings.callbacks` list, after the zone hook. Its source is `config/gateway/koodam_guard.py`. A pod must not turn off a policy of the operator:

- The chart sets `litellm_settings.allow_dynamic_callback_disabling: false` when the guard is on. Thus the `x-litellm-disable-callbacks` header and the `litellm_disabled_callbacks` field of a key or a request do not turn off the hook.
- The pinned image also honours these fields only with an enterprise licence. The image check sends them and a `guardrails` field, and the hook still runs.
- The chart does not use the LiteLLM custom guardrail form. The `callbacks` form gives the same result, and the image check proves it.

## What the guard examines

The untrusted text enters the model in the next request after a tool call or a read. The harness sends the full history again on each call. The guard examines only the blocks of the sources that the policy names:

| Source | Anthropic Messages | Chat completions | Responses |
| --- | --- | --- | --- |
| `toolResult` | a `tool_result` block | a message with `role: tool` | a `function_call_output` item |
| `document` | a `document` block | none | none |

The guard does not examine the system prompt or the text of the user. The operator and the user own that text, and it is full of instructions.

## The detector

The detector is one function, `detect(text)`. It gives a list of rule ids. Each rule is a fixed pattern. The rule set has a version, and a change of a rule gives a new version.

| Rule id | The form that it finds |
| --- | --- |
| `override` | A phrase that tells the model to ignore or replace its earlier instructions, and its near forms. |
| `role-marker` | A false role or turn marker in the text, for example `<system>`, `Human:`, `Assistant:` or a false tool call block. |
| `send-data` | An instruction to send data, files or secrets to a URL or a host. |
| `hidden-text` | Text that a person does not see: zero-width characters, Unicode tag characters, or an HTML comment with an instruction. |

The detector uses no model and no external call. A match names its rule, and a test of a rule is exact.

## The policy

The operator sets one policy for the install in the chart values:

| Value | Default | Meaning |
| --- | --- | --- |
| `gateway.bundled.guard.enabled` | `false` | Adds the hook to the gateway. With `false`, the gateway configuration does not change. |
| `gateway.bundled.guard.action` | `record` | `record`, `mark` or `block`. See "Actions". |
| `gateway.bundled.guard.sources` | `[toolResult, document]` | The sources that the guard examines. |
| `gateway.bundled.guard.maxBytes` | `65536` | The guard examines at most this count of bytes of each block. |

The chart gives the policy to the hook in the variable `KOODAM_GUARD_POLICY`. The policy version is the rule set version (`r1`) and the first 12 hex characters of the SHA-256 of the policy. A change of the policy starts the gateway pod again. The chart refuses a policy value that is not valid, and the hook also stops the gateway at its start.

## Actions

| Action | The request | The provider |
| --- | --- | --- |
| `record` | The gateway sends the request with no change. | gets the request |
| `mark` | The gateway puts one fixed notice line before each matched block, then sends the request. | gets the request with the notice |
| `block` | The gateway refuses the request with status 403 and the reason `UntrustedInstruction`. The reply has the rule ids and the digest, and no content. | gets no request |

For each action, the guard writes one log line (see "The log line").

- **`mark`.** The notice is a fixed function of the policy version and the rule ids. For a block with text parts, it goes before the first part. Thus the next calls of the run send the same text, and the prompt cache of the provider stays valid after the first call. The notice adds input tokens to the spend.
- **`block`.** The matched block stays in the history of the run, and the harness sends it again. Thus each later model call of that run also gets 403. A hostile page can stop a run, and a false match also stops a run.
- **A hook error.** If the hook itself fails, it writes a line with the outcome `error` and sends the request (fail open). The guard is one layer, and the egress policy is the boundary.

## The log line

The gateway writes 1 JSON line to its stdout for each new block that the guard examines. It also writes a line for a block with no match, so the line is evidence that the policy examined the content.

### Example

```json
{"v":1,"kind":"gateway-guard","time":"2026-11-26T09:14:02.118Z","outcome":"recorded","action":"record","policy":"r1-5d41402abc4b","rules":["send-data"],"source":"toolResult","digest":"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","bytes":2214,"truncated":false,"namespace":"team-a","keyAlias":"tk-t1-0f3c","model":"model-a"}
```

### Fields

| Field | Type | Always | Value |
| --- | --- | --- | --- |
| `v` | number | yes | The contract version. It is `1` for this page. |
| `kind` | string | yes | `gateway-guard`. |
| `time` | string | yes | The time of the decision, RFC 3339 in UTC. |
| `outcome` | string | yes | `clean`, `recorded`, `marked`, `blocked` or `error`. See "Outcomes". |
| `action` | string | yes | The action of the policy: `record`, `mark` or `block`. |
| `policy` | string | yes | The policy version: the rule set version and a short digest of the rendered policy. |
| `rules` | array of strings | yes | The ids of the rules that matched. It is empty for `clean` and `error`. Each id comes from the fixed set in "The detector". |
| `source` | string | yes | `toolResult` or `document`. It is `request` for `error`. |
| `digest` | string | yes | `sha256:` and the hex SHA-256 of the UTF-8 bytes that the guard examined. A block with 2 or more text parts is the parts with a newline between them. It is empty for `error`. |
| `bytes` | number | yes | The length of the block in bytes, before the `maxBytes` limit. It is `0` for `error`. |
| `truncated` | boolean | yes | `true` if the block is longer than `maxBytes`. The guard examined only the first `maxBytes` bytes. |
| `namespace` | string | no | The namespace from the key metadata. |
| `keyAlias` | string | no | The alias of the gateway key, for example `kd-<uid>`, `ws-...` or `tk-<testID>-<uid>`. It is not a credential. |
| `model` | string | no | The model name of the request. |
| `reason` | string | no | Only for `error`. The fixed code `hook-error`, not a message from the request. |

"Always" means that the field is in each line. A field that is not always there is not in the line when it is empty.

The line has no content text, no key value and no tool name, because the pod chooses the tool name.

### Outcomes

| Outcome | Meaning |
| --- | --- |
| `clean` | No rule matched. The gateway sent the request. |
| `recorded` | A rule matched, and the action is `record`. The gateway sent the request with no change. |
| `marked` | A rule matched, and the action is `mark`. The gateway sent the request with the notice. |
| `blocked` | A rule matched, and the action is `block`. The gateway refused the request with 403. |
| `error` | The hook failed. The gateway sent the request with no change. |

### Repeated blocks

The hook keeps a bounded cache of the digests that it examined for each key. A block with a known digest for the same key gives no second line. For `mark` and `block`, the hook still applies the action to that block. The cache has at most 4096 entries. After a restart of the gateway, the cache is empty, and the hook writes the lines again.

### Version rules

- An added field keeps `v` the same. A reader must ignore a field that it does not know.
- A removed field, a renamed field or a new meaning of a field adds 1 to `v`.
- A new rule id or a new rule set version does not change `v`. A reader must accept a rule id that it does not know.

### Read the lines

The lines are in the log of the gateway pod, in the namespace of the install. The operator reads them, for example with `kubectl logs` of the gateway Deployment. A reader matches a line to a test by `keyAlias` and `time`. A reader for Nookku is tracked in [mohanraj00/nookku#700](https://github.com/mohanraj00/nookku/issues/700).

**Protect the lines in the same way as the pod logs.** The line has no content, but a reader can compare the digest of a short text with the digest of a guess, and so confirm the guess.

## What the guard cannot detect

The guard finds only the forms that its rules name. It does not find:

- a paraphrase of a rule form, or an instruction in a different language;
- encoded text, for example base64, a cipher or a different character set;
- an instruction that is split across 2 or more blocks or calls;
- text in an image, an audio file or a binary document;
- an instruction that reads as normal content, for example a README step that tells the reader to run a script;
- text after the first `maxBytes` bytes of a block;
- text in a source that the policy does not name, or in the system prompt or the user text;
- the model calls of a pod with no gateway key, for example a Workspace with a subscription login. These calls go in a TLS tunnel to the provider, and no part of Koodam can read them;
- the model calls through your own gateway (`gateway.mode: existing`). The hook is a part of the bundled gateway only.

A coding agent reads many texts that are full of instructions. Thus a wide rule gives false matches, and the rules stay narrow. A narrow rule misses more forms.

Other limits:

- The guard adds time to each model call. The bundled gateway has 1 replica and 1 worker, so a slow hook delays each model call of the install.
- The policy is one policy for the install. A stricter action for each namespace is a possible follow-up ([#439](https://github.com/mohanraj00/koodam/issues/439)).
- A detector with a classifier model is a possible follow-up. It starts with a spike that measures its speed, its matches and the licence of its weights ([#438](https://github.com/mohanraj00/koodam/issues/438)).

## Turn on the guard

1. Install or upgrade the chart with `--set gateway.bundled.guard.enabled=true`. The default action is `record`, so the gateway does not change a request.
2. Read the lines of the gateway:

   ```sh
   kubectl -n koodam-system logs deployment/koodam-litellm | grep '"kind":"gateway-guard"'
   ```

3. Examine the `recorded` lines for false matches with your agents. Then set `gateway.bundled.guard.action` to `mark` or `block` if you want.

A change of a guard value starts the gateway pod again. [The chart README](../charts/koodam/README.md) has each value.

## Tests

- `tests/test_guard_hook.py`: a planted injection and a clean reply in each request form, for each action. The same text in the system prompt or the user text gives no line. Also the `maxBytes` limit, the scan of a large block in pieces, the cache, a bad policy and a hook error.
- `tests/testdata/guard/`: the measured set. It has 5 planted texts, one or more for each rule, and 4 clean texts, also a README with many instructions. The test asserts that each planted text matches its rule and that no clean text matches.
- `test/chart/guard_test.go`: with the guard off, the gateway configuration does not change. With the guard on, the gateway has the hook, the registration, the policy and `allow_dynamic_callback_disabling: false`. With the zone hook, the zone hook is first.
- `test/e2e/egress_test.go` (kind, action `record`): an Agent pod sends a planted and a clean tool reply. The gateway sends both calls and writes a `recorded` and a `clean` line with the key alias and the namespace of the Agent. The planted reply names a host on port 8443, and the egress proxy refuses the call to it.

### Check the pinned gateway image

`make guard-image-check` runs the hook in the gateway image of the chart, in docker, with a fake upstream model in the same container. It uses no model and no network access. For each action, it sends a planted and a clean text in the 4 request forms (chat completions, Anthropic Messages with a tool reply, Anthropic Messages with a text document, and Responses), with and without a stream. It also sends 4 requests that try to turn off the hook. It checks the status, the body that the upstream gets and the line of each request. The output has the names of the cases, not the texts.

Run the check again before a change of the pinned image. To save the result, add `--results docs/results/guard-image-check.json` to the command of the target. The result for `litellm-database:v1.104.2` on 2026-11-26: 60 cases, no failures ([results file](results/guard-image-check.json), with the image digest and the source commit).

- `block`: each planted request gets 403, and the upstream gets no request. Each clean request gets 200.
- `mark`: the upstream gets each planted block with 1 notice line.
- `record`: the upstream gets each request with no change.

The image sends `/v1/messages` to an Anthropic model as it is. For an OpenAI model, the pinned image removes a text document block before it sends the request. The guard examines the block before this change, so its line does not depend on the model.
