# Entelos signed record vectors

`entelos-v0.5.0-signed-cases.json` comes from Entelos tag `v0.5.0`, path
`src/protocol/signed/vectors/signed-cases.json`. The source licence is Apache-2.0.

The source file has the private key seeds of the test keys in its top-level `keys` object. This
copy does not have that object. No other byte is changed.

| File | SHA-256 |
|---|---|
| The source file | `05498e56d1c1a9a3d11da45a7b7d56d1b4abdf407c824f7eb5169166a8a0b90b` |
| This copy | `f968adc4a59c69e9af17a20284c168b6980b5d4b4c02d53fbedb2eccf968754a` |

The strip step, with jq 1.7.1, on the source file:

```sh
jq --indent 1 'del(.keys)' signed-cases.json > entelos-v0.5.0-signed-cases.json
```

The source file has the jq format with an indent of 1, so the step removes only the lines of the
`keys` object. The `note` field of the source still says that the seeds are public. This copy
keeps the note without change.

`TestSignedVectors` pins the SHA-256 of this copy. `TestVectorsHaveNoSeeds` fails if the file
has the top-level `keys` object or a `seed` field.
