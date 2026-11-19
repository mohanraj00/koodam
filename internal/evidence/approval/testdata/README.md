# Entelos signed record vectors

`entelos-v0.5.0-signed-cases.json` is a copy of
`src/protocol/signed/vectors/signed-cases.json` from Entelos tag `v0.5.0`.
The source licence is Apache-2.0.

The file is verbatim, because `TestSignedVectors` pins its SHA-256. Thus it keeps the `seed`
values of the source project. These seeds are public test values of Entelos, for the test keys
of the vectors only. They are not a credential of Koodam or of a user. No test reads the top-level `keys`
object or a seed: the tests read only `key_sets` and `cases`. Do not use these keys to sign a
real decision.
