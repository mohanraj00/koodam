"""Check that the review requests in docs/REVIEW.md are the requests that start the Claude review workflow."""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))
import claude_review  # noqa: E402


class ReviewDocTest(unittest.TestCase):
    def test_the_documented_requests_start_the_workflow(self):
        rules = (ROOT / 'docs/REVIEW.md').read_text()
        workflow = (ROOT / '.github/workflows/claude-review.yml').read_text()
        requests = set(re.findall(r'`(@claude [^`]+)`', rules))
        self.assertEqual(requests, set(claude_review.REQUESTS.values()))
        self.assertEqual(set(re.findall(r"body == '([^']+)'", workflow)), requests)


if __name__ == '__main__':
    unittest.main()
