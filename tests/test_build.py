import json
import tempfile
import unittest
from unittest.mock import patch
import subprocess
from booking_tools import maintain
from datetime import datetime, timedelta, timezone
from booking_tools.maintain import release_freeze_active
from pathlib import Path
from booking_tools.build import digest, source_fingerprint, verified_build_id


class BuildProvenanceTests(unittest.TestCase):
    def test_added_changed_or_removed_go_file_invalidates_build(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "main.go").write_text("package main")
            (root / "orders.go").write_text("package main\n// original")
            (root / "booking.exe").write_bytes(b"binary")
            fingerprint, files = source_fingerprint(root)
            manifest = {"schemaVersion":3,"sourceSHA256":fingerprint,"sourceFiles":files,"binarySHA256":digest(root/"booking.exe")}
            self.assertEqual(verified_build_id(root, manifest), fingerprint)
            (root / "orders.go").write_text("package main\n// changed")
            with self.assertRaises(ValueError): verified_build_id(root, manifest)
            (root / "orders.go").write_text("package main\n// original")
            (root / "extra.go").write_text("package main")
            with self.assertRaises(ValueError): verified_build_id(root, manifest)
            (root / "extra.go").unlink()
            (root / "orders.go").unlink()
            with self.assertRaises(ValueError): verified_build_id(root, manifest)

    def test_build_freeze_uses_beijing_time_and_both_boundaries(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/"booking.json").write_text(json.dumps({"ReleaseTime":"12:00:00","WindowSeconds":90}))
            cn=timezone(timedelta(hours=8))
            for clock, expected in (("11:44:59",False),("11:45:00",True),("12:00:00",True),("12:01:40",True),("12:01:41",False)):
                now=datetime.fromisoformat("2026-10-20T"+clock).replace(tzinfo=cn).astimezone(timezone.utc)
                self.assertEqual(release_freeze_active(root,now),expected)

    def test_legacy_manifest_cannot_validate_split_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("main.go", "orders.go", "booking.exe"):
                (root / name).write_bytes(b"example")
            with self.assertRaises(ValueError):
                verified_build_id(root, {"binarySHA256":digest(root/"booking.exe"),"mainGoSHA256":digest(root/"main.go")})


    def test_python_changes_and_nested_go_are_tracked_but_runtime_is_not(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "cmd/booking").mkdir(parents=True)
            (root / "booking_tools").mkdir()
            (root / "runtime").mkdir()
            (root / "cmd/booking/main.go").write_text("package main")
            (root / "booking_tools/tool.py").write_text("original")
            original, files = source_fingerprint(root)
            self.assertIn("cmd/booking/main.go", files)
            self.assertIn("booking_tools/tool.py", files)
            (root / "runtime/scratch.py").write_text("not source")
            self.assertEqual(source_fingerprint(root)[0], original)
            (root / "booking_tools/tool.py").write_text("changed")
            self.assertNotEqual(source_fingerprint(root)[0], original)

    def test_failed_check_invalidates_previous_pass_without_replacing_binary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "runtime").mkdir()
            (root / "booking.exe").write_bytes(b"previous")
            report = root / "runtime/local-validation.json"
            report.write_text(json.dumps({"passed": True}))
            with patch.object(maintain, "ROOT", root), patch.object(maintain, "find_tool", return_value="go"), \
                 patch.object(maintain.subprocess, "check_output", return_value=""), \
                 patch.object(maintain, "run", side_effect=subprocess.CalledProcessError(1, ["go", "test"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    maintain.run_quality("check")
            self.assertFalse(json.loads(report.read_text())["passed"])
            self.assertEqual(json.loads(report.read_text())["status"], "failed")
            self.assertEqual((root / "booking.exe").read_bytes(), b"previous")

if __name__ == "__main__":
    unittest.main()
