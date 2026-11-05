"""Maintenance CLI integration checks use only temporary data and offline mocks."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import main


class MaintenanceCLITests(unittest.TestCase):
    def test_help_and_invalid_login_work_from_other_directory(self):
        entry = Path(main.__file__).resolve()
        with tempfile.TemporaryDirectory() as directory:
            for arguments, code in ((["--help"], 0), (["login", "--account", "../bad"], 2)):
                result = subprocess.run([sys.executable, str(entry), *arguments], cwd=directory,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, code)

    def test_health_uses_explicit_config_without_login_or_network(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "booking.json"
            config.write_text(json.dumps({"StateDir":"runtime", "Accounts":[
                {"Name":"account-01", "CookieFile":"missing.json"}]}))
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(main.main(["health", "--config", str(config)]), 0)
            result = json.loads(output.getvalue())
            self.assertEqual(result["requests"], 0)
            self.assertEqual(result["results"][0]["status"], "session_missing")

    def test_review_generates_both_reports_and_preserves_human_decision(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / "runtime/research/2099-01-01"
            folder.mkdir(parents=True)
            decision = folder / "decision.json"
            decision.write_text('{"status":"reviewed","action":"retain"}')
            before = decision.read_bytes()
            with patch.object(main, "ROOT", root), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(main.main(["review", "--date", "2099-01-01"]), 0)
            self.assertFalse(json.loads((root / "runtime/report-2099-01-01.json").read_text())["eventFilePresent"])
            self.assertIn("missing_event_file", json.loads((folder / "review.json").read_text())["issues"])
            self.assertEqual(decision.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
