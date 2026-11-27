#!/bin/sh
# Run this test with sh images/nookku/test/launcher_test.sh.
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/lib/nookku" "$work/project"

# Use the test lib from the project folder.
sed 's|/koodam/nookku/lib|../lib|g' "$here/nookku" > "$work/launcher"
: > "$work/lib/nookku/__init__.py"
cat > "$work/lib/nookku/__main__.py" <<'PY'
import os
from pathlib import Path
import subprocess
import sys


def main():
    assert sys.flags.isolated and sys.flags.no_site, "The launcher needs -I -S"
    assert os.environ.get("PYTHONPATH") == "../lib", "The launcher did not export the lib path"
    assert os.environ.get("PYTHONSAFEPATH") == "1", "The launcher did not export PYTHONSAFEPATH=1"
    assert os.environ.get("PYTHONNOUSERSITE") == "1", "The launcher did not export PYTHONNOUSERSITE=1"
    subprocess.run([sys.executable, "-m", "nookku", "--version"], check=True)


if __name__ == "__main__":
    assert sys.argv[1:] == ["--version"]
    print(Path(__file__).resolve())
PY

expected=$(cd "$work/lib/nookku" && pwd -P)/__main__.py
check_pinned_import() {
    actual=$(sh ../launcher)
    if [ "$actual" != "$expected" ]; then
        printf 'Expected the child to import %s, but it imported %s\n' "$expected" "$actual" >&2
        exit 1
    fi
}

cd "$work/project"
unset PYTHONPATH PYTHONSAFEPATH PYTHONNOUSERSITE
check_pinned_import
printf 'PASS: The launcher exports the variables and the child imports the pinned lib.\n'

if ! python3 -I -S -c 'import sys; sys.exit(sys.version_info < (3, 11))'; then
    printf 'FAIL: This test needs python3 3.11 or later on the PATH.\n' >&2
    exit 1
fi
mkdir nookku
: > nookku/__init__.py
printf 'print(__file__)\n' > nookku/__main__.py
check_pinned_import
printf 'PASS: The child imports the pinned lib with a nookku package in the current folder.\n'

# A python3 earlier than 3.11 fails the version check. The launcher must refuse to start.
mkdir "$work/old"
printf '#!/bin/sh\nexit 1\n' > "$work/old/python3"
chmod 0755 "$work/old/python3"
if err=$(PATH="$work/old:$PATH" sh ../launcher 2>&1); then
    printf 'FAIL: The launcher started with a python3 earlier than 3.11.\n' >&2
    exit 1
fi
case $err in
*"needs python3 3.11 or later"*) ;;
*) printf 'FAIL: The launcher gave the wrong message: %s\n' "$err" >&2; exit 1 ;;
esac
printf 'PASS: The launcher refuses a python3 earlier than 3.11.\n'
