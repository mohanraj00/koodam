# Gate image of the kind test

`test/e2e/gate_test.go` builds this image and loads it into kind. The image has the Nookku release of the pin in `internal/evidence/nookku.pin` and the gate project in `project/`.

- `project/.nookku/config.json` names the entry. The project uses no model harness.
- `project/.nookku/egress.json` is the egress answer of the project. The run step of the gate Job registers it before the run. The entry calls only the app of the version, so the answer has no part.
- `project/entry.py` sends each message to the app, at the address in `KOODAM_GATE_TARGET`.
- `project/gates/pass.json` has one test with 2 different messages. The app gives 2 different replies, so the test passes the limit `repeated_reply: 0`.
- `project/gates/fail.json` has one test with the same message 2 times. The app gives the same reply 2 times, so the test breaks the limit `repeated_reply: 0`, and the verdict is `fail`.
