# Operations page

This package holds the operations page of the spend API and the mugham kit files that the page uses.

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
