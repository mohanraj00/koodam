"""Fetch the pinned Nookku files for the Nookku data image (#83).

The wheel comes from PyPI and must have the SHA-256 of internal/evidence/nookku.pin. Each plugin
file comes from plugins/nookku at the tag commit and must have its SHA-256 in plugin.sha256. The
script writes the wheel content to OUT/share/lib and the plugin files to OUT/share/plugin. It runs
no Nookku code.
"""

import hashlib
import io
import os
import pathlib
import sys
import urllib.request
import zipfile

WHEEL = "https://files.pythonhosted.org/packages/py3/n/nookku/nookku-{0}-py3-none-any.whl"
PLUGIN = "https://raw.githubusercontent.com/mohanraj00/nookku/{0}/plugins/nookku/{1}"


def fetch(url, digest):
    with urllib.request.urlopen(url, timeout=60) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != digest:
        raise SystemExit("wrong SHA-256: " + url)
    return data


def safe(root, name):
    path = (root / name).resolve()
    if name.startswith("/") or ".." in pathlib.PurePosixPath(name).parts or root.resolve() not in path.parents:
        raise SystemExit("unsafe path: " + name)
    return path


def main(version, wheel_sha256, commit, hashes, out):
    out = pathlib.Path(out)
    lib, plugin = out / "share" / "lib", out / "share" / "plugin"
    with zipfile.ZipFile(io.BytesIO(fetch(WHEEL.format(version), wheel_sha256))) as wheel:
        for item in wheel.infolist():
            if item.is_dir():
                continue
            path = safe(lib, item.filename)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(wheel.read(item))
    for line in pathlib.Path(hashes).read_text().splitlines():
        digest, name = line.split(None, 1)
        path = safe(plugin, name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(fetch(PLUGIN.format(commit, name), digest))
        if name.endswith(".sh"):
            os.chmod(path, 0o755)


if __name__ == "__main__":
    main(*sys.argv[1:])
