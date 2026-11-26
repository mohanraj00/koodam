"""Make a sealed gate folder from one gate case of the pinned Nookku release.

The seam test of the evidence gate uses this program (docs/evidence-gate.md, "Seam test"). A case
of conformance/gate/ has the gate file and the records of each test, and not a sealed gate
folder. This program makes the folders with the functions of the pinned Nookku package, as
tests/test_gate.py of that release does. Then the fixed verify program of the gate Job can read
the result.

Arguments: the case folder, the project root and the gate id. The option --tamper changes
result.json after the seal. NOOKKU_HOME names the folder of the seal copies.
"""

import json
import sys
from pathlib import Path

from nookku import gate, record, script, seal, state, verdict


def records(folder: Path, messages: list, oks: list, tap: list | None) -> None:
    relay = record.Writer(folder / "relay.jsonl")
    for said, ok in zip(messages, oks, strict=True):
        relay.append({"type": "turn", "harness": "script", "said": said, "shown": "", "ok": ok})
    if tap is None:
        return
    exchanges = record.Writer(folder / "tap.jsonl")
    for said, reply in zip(messages, tap, strict=True):
        exchanges.append({"type": "exchange", "input": said, "status": 200, "reply": reply})


def sealed_test(folder: Path, given: dict) -> bytes:
    """A sealed test folder with the records of a case. It gives back the bytes of the script.

    A script message can have the key during, and findings.json can have second_messages
    (Nookku 0.6.0).
    """
    folder.mkdir(parents=True)
    during = given.get("during") or [None] * len(given["messages"])
    rows = [
        {"said": m} if after is None else {"said": m, "during": after}
        for m, after in zip(given["messages"], during, strict=True)
    ]
    body = "".join(json.dumps(r) + "\n" for r in rows).encode()
    manifest = {"test": folder.name, "entry": given["entry"], "entry_name": given["entry_name"]}
    manifest.update(policy={}, config_sha256={}, spec=[])
    manifest["script"] = {"source": None, "file": "script.jsonl", "sha256": script.sha256(body)}
    manifest["verdict"] = {"limits": given["limits"], "unread": []}
    (folder / "manifest.json").write_text(json.dumps(manifest))
    findings = {"counts": given["counts"]}
    if "second_messages" in given:
        findings["second_messages"] = given["second_messages"]
    (folder / "findings.json").write_text(json.dumps(findings))
    verdict.build(folder)
    (folder / "audit.json").write_text(json.dumps(given["audit"]))
    records(folder, given["messages"], given["replies"], given.get("tap"))
    (folder / "script.jsonl").write_bytes(body)
    seal.write(folder)
    return body


def main(case: Path, root: Path, gate_id: str, tamper: bool) -> int:
    given = json.loads((case / "case.json").read_text(encoding="utf-8"))
    data = (case / "gate.json").read_bytes()
    tests_dir = state.tests_dir(root)
    for base, found in given.get("bases", {}).items():
        (tests_dir / base).mkdir(parents=True)
        records(tests_dir / base, found["messages"], [True] * len(found["messages"]), found["tap"])
        seal.write(tests_dir / base)
    folder = gate.folder_of(root) / gate_id
    folder.mkdir(parents=True)
    (folder / "gate.json").write_bytes(data)
    inputs, rows = {}, []
    for test in gate.parse(data):
        name, replay = test["name"], test["replay"]
        pin = None if replay is None else gate.base_pin(tests_dir, replay["base"])
        (folder / "inputs" / name).mkdir(parents=True)
        if name not in given["tests"]:
            rows.append(gate.ledger_row(name, None, given["errors"][name], None, pin))
            continue
        inputs[name] = {"script.jsonl": sealed_test(tests_dir / f"t-{name}", given["tests"][name])}
        (folder / "inputs" / name / "script.jsonl").write_bytes(inputs[name]["script.jsonl"])
        if "persona" in given["tests"][name]:
            inputs[name]["persona"] = given["tests"][name]["persona"].encode("utf-8")
            (folder / "inputs" / name / "persona").write_bytes(inputs[name]["persona"])
        pinned = {"entry": given["tests"][name]["entry"], "policy": {}, "spec": [], "config_sha256": {}}
        rows.append(gate.ledger_row(name, tests_dir / f"t-{name}", None, pinned, pin))
    ledger = {"v": 1, "gate": gate_id, "subject": given["subject"], "tests_dir": str(tests_dir), "tests": rows}
    copies = {}
    if "proofs" in json.loads(data):
        ledger["proofs"] = []
        for n, proof in enumerate(gate.parse_proofs(data), 1):
            found = given["proofs"][proof["path"]]
            body = None if found["text"] is None else found["text"].encode("utf-8")
            ledger["proofs"].append(
                gate.proof_row(proof["path"], body, found["modified"], found["checked"], found["error"])
            )
            if body is not None:
                copies[n] = body
                (folder / "proofs").mkdir(exist_ok=True)
                (folder / "proofs" / str(n)).write_bytes(body)
    (folder / "run.json").write_text(record.json_text(ledger, indent=1) + "\n", encoding="utf-8")
    result = gate.compute(gate_id, data, inputs, ledger, gate.read_evidence(ledger), copies)
    expect = json.loads((case / "expect_result.json").read_text(encoding="utf-8"))
    if result["verdict"] != expect["verdict"] or result["reasons"] != expect["reasons"]:
        print("The result of the case is not the expected result of the release.", file=sys.stderr)
        return 1
    (folder / "result.json").write_text(record.json_text(result, indent=1) + "\n", encoding="utf-8")
    seal.write(folder)
    if tamper:
        result["verdict"] = "pass" if result["verdict"] == "fail" else "fail"
        (folder / "result.json").write_text(record.json_text(result, indent=1) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main(Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3], "--tamper" in sys.argv[4:]))
