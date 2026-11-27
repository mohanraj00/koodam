# Operations page

This package holds the operations page of the spend API and the mugham kit files that the page uses.

## Run view

When a viewer selects a run, the page shows the run view. `static/page.js` reads these paths of the
spend API, with the token in the `Authorization` header:

- `GET /v1/runs/{namespace}/{name}`: the Job, its pods and their containers, and the status conditions of the
  Agent and of the Job. The server redacts each condition message.
- `GET /v1/runs/{namespace}/{name}/log?lines=200`: the last `LOG_LINES` lines of the agent container. The page
  shows the lines that the server sends as text, with `textContent`. If the log has more lines, or if the API
  gives an error, for example the time limit, the status above the log tells you.
- `GET /v1/runs/{namespace}/{name}/watch`: the live watch. The page reads the event stream with `fetch` and a
  stream reader, because `EventSource` cannot send the `Authorization` header. The page shows the phase of
  each `status` event at once. If the phase changes, the page reads the run and the log again.

The watch connects again after the `retry` time of the stream, when the stream ends or the network fails.
After status 429, it waits for the `Retry-After` time. If the page gets no byte for `WATCH_IDLE_MS`, it
connects again. If the watch is not available, the page reads the run and the log again at each refresh.
The page stops the watch with an `AbortController` when the viewer selects a different run, when the run
leaves the list, or when the viewer forgets the token.

## Pinned kit

`static/kit/` holds files from the `mugham.tar` asset of the mugham release `v0.7.0`, with no change.
`mugham.pin.json` records the release, the SHA-256 value of the asset and the SHA-256 value of each file.
The kit is under the Apache License 2.0. Its `LICENSE` file is in `static/kit/`.

The pin has only the files that the page imports: the import closure of `src/kit.js` and
`src/components/index.js`, the JSON files that they import, `src/tokens/tokens.css`,
`src/styles/page.css` and `LICENSE`. `TestKitMatchesPin` fails if a file is missing, extra or changed.

To check the pin against the release:

1. Download the release asset and its checksum file:
   `gh release download v0.7.0 -R mohanraj00/mugham -p 'mugham.tar*'`.
2. Run `shasum -a 256 -c mugham.tar.sha256`. The value must be the `sha256` value of the pin.
3. Extract the archive. Each value in `files` of the pin must be the value of the same path in
   `mugham/manifest.json`.

To change the kit version, do the 3 steps for the new release. Then copy the files of the new
closure into `static/kit/`, write the new values into the pin and run `go test ./internal/opspage`.
