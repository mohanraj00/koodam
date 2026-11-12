"""Find a stale version string in the README and the docs."""
import pathlib
import re
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent


class VersionStrings(unittest.TestCase):
    def test_install_versions_match_the_current_release(self):
        readme = (ROOT / "README.md").read_text()
        found = re.search(r"The current release is v(\d+\.\d+\.\d+)\.", readme)
        self.assertIsNotNone(found, "README.md must say: The current release is vX.Y.Z.")
        current = found.group(1)
        files = [ROOT / "README.md", *sorted((ROOT / "docs").glob("*.md"))]
        for path in files:
            for number, line in enumerate(path.read_text().splitlines(), 1):
                m = re.match(r"\s*VERSION=(\d+\.\d+\.\d+)\s*$", line)
                if m:
                    self.assertEqual(
                        m.group(1), current,
                        f"{path.relative_to(ROOT)}:{number} has a stale version")


if __name__ == "__main__":
    unittest.main()
