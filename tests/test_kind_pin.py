"""Check that the e2e job of CI and the samples job of the release use the same kind pin."""
import pathlib
import re
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
NAMES = ("KIND_VERSION", "KIND_NODE_IMAGE")


def pins(path):
    text = (ROOT / path).read_text()
    found = {}
    for name in NAMES:
        values = re.findall(rf"^\s+{name}:\s*(\S+)\s*$", text, re.MULTILINE)
        if len(values) != 1:
            raise AssertionError(f"{path} must set {name} 1 time, not {len(values)} times")
        found[name] = values[0]
    return found


class KindPin(unittest.TestCase):
    def test_the_release_samples_use_the_kind_pin_of_ci(self):
        ci = pins(".github/workflows/ci.yml")
        release = pins(".github/workflows/release.yml")
        self.assertEqual(release, ci, "docs/kind-pin.md says how to update the pin")
        self.assertIn("@sha256:", ci["KIND_NODE_IMAGE"], "the node image must have its digest")


if __name__ == "__main__":
    unittest.main()
