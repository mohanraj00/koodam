/*
Copyright 2026 Mohan Raj.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package evidence

// The two fixed programs of the gate Job. They are Go constants and not files, so the manager
// image needs no script file. The gate image must have python3 with the Nookku package. The
// controller gives each program to python3 -c, with the values of the spec as separate
// arguments. Thus no value of the spec goes through a shell.

// RunScript is the fixed program of the gate container. Its arguments are the project root in the
// image, the folder for the copy, the gate file and the subject. The project must have its egress
// answer in .nookku/egress.json (section 7.11 of the Nookku SPEC.md).
const RunScript = `"""The fixed run step of the Koodam gate Job (docs/evidence-gate.md).

Arguments: the project root in the image, the folder for the copy, the gate file and the subject.
Nookku writes the tests and the gate folder in the project, so the step copies the project to the
work volume first. Nookku accepts an egress answer only for 30 minutes, so the step registers the
answer of the project, .nookku/egress.json, before the run. The step does not use the exit code
of the run: the verify step decides.
"""

import pathlib
import shutil
import subprocess
import sys


def main(src: str, work: str, gate_file: str, subject: str) -> int:
    shutil.copytree(src, work, symlinks=True)
    nookku = [sys.executable, "-m", "nookku"]
    answer = pathlib.Path(work, ".nookku", "egress.json")
    if answer.is_file():
        subprocess.run([*nookku, "register", "--file", str(answer), "--root", work], cwd=work, check=False)
    argv = [*nookku, "gate", "run", gate_file, "--subject", subject, "--root", work, "--json"]
    subprocess.run(argv, cwd=work, check=False)
    return 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:5]))
`

// VerifyScript is the fixed program of the verify container. Its arguments are the folder of the
// project copy and the file for the report.
const VerifyScript = `"""The fixed verify step of the Koodam gate Job (docs/evidence-gate.md).

Arguments: the folder of the project copy, and the file for the report. The step runs
nookku gate verify --json on the newest gate folder and prints its output, so the full result
stays in the pod log. It writes a short JSON report for the controller: the exit code, the gate
id, the subject, the verdict, the SHA-256 of result.json, the format version of result.json and
the Nookku version. The fields of a result that cannot be used stay empty.
"""

import hashlib
import json
import pathlib
import subprocess
import sys
from importlib import metadata


def main(root: str, out: str) -> int:
    argv = [sys.executable, "-m", "nookku", "gate", "verify", "--root", root, "--json"]
    done = subprocess.run(argv, capture_output=True, text=True, check=False)
    sys.stdout.write(done.stdout)
    sys.stderr.write(done.stderr)
    report = {"v": 1, "exit": done.returncode, "gate": "", "subject": "", "verdict": ""}
    report.update(sha256="", format=0, nookku="")
    try:
        report["nookku"] = metadata.version("nookku")
        data = json.loads(done.stdout)
        folder, result = pathlib.Path(data["gate"]), data["result"]
        report.update(gate=folder.name, subject=result["subject"], verdict=result["verdict"])
        report.update(format=result["v"])
        report["sha256"] = hashlib.sha256((folder / "result.json").read_bytes()).hexdigest()
    except (ValueError, KeyError, TypeError, OSError, metadata.PackageNotFoundError):
        pass  # The controller refuses a report with an empty field, unless the exit code is 2.
    pathlib.Path(out).write_text(json.dumps(report), encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2]))
`
