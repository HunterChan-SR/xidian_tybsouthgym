import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError, URLError
from booking_tools.health import check, inspect_session, request_status


class AccountHealthTests(unittest.TestCase):
    def fixture(self, root, cookies):
        accounts = []
        for index, cookie in enumerate(cookies):
            name = "account-%02d" % (index + 1)
            path = name + ".json"
            (root / path).write_text(json.dumps({"Cookie": cookie}), encoding="utf-8")
            accounts.append({"Name": name, "CookieFile": path})
        config = root / "booking.json"
        config.write_text(json.dumps({"Accounts": accounts, "StateDir": "runtime"}), encoding="utf-8")
        return config

    def test_expired_duplicate_does_not_hide_new_valid_session(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = self.fixture(root, ["UserId=SECRET;token=OLD", "UserId=SECRET;token=NEW", "UserId=OTHER", "UserId=OTHER"])
            calls = []
            def probe(session, endpoint):
                calls.append(endpoint)
                if "OLD" in session["Cookie"]:
                    return "ok", "0"
                return "ok", "1" if "CheckUserStatus" in endpoint else '{"limitcount":2}'
            report = check(config, probe, lambda _: None)
            self.assertEqual(report["uniqueUsableIdentities"], 2)
            self.assertEqual(report["results"][0]["status"], "login_required")
            self.assertEqual(report["results"][3]["duplicateOf"], "account-03")
            self.assertTrue(report["changed"])
            self.assertFalse(check(config, probe, lambda _: None)["changed"])
            text = (root / "runtime/account-health.json").read_text(encoding="utf-8")
            self.assertNotIn("SECRET", text)
            self.assertTrue(all("CheckUserStatus" in p or "GetOrderInfo" in p for p in calls))

    def test_transient_failure_does_not_mean_expired(self):
        for status in ("network_error", "server_error", "http_error", "rate_limited"):
            self.assertEqual(inspect_session({}, lambda *_: (status, None)), (status, None))
        self.assertEqual(inspect_session({}, lambda *_: ("ok", "<html>"))[0], "unexpected_response")

    def test_rate_limit_stops_checks_across_accounts(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.fixture(Path(directory), ["UserId=1", "UserId=2"])
            report = check(config, lambda *_: ("rate_limited", None), lambda _: None)
            self.assertEqual(report["requests"], 1)
            self.assertEqual(report["results"][1]["status"], "deferred_rate_limit")

    def test_booking_lock_defers_without_overwriting_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = self.fixture(root, ["UserId=1"])
            (root / "runtime").mkdir()
            (root / "runtime/runner.lock").touch()
            result = check(config, lambda *_: self.fail("Unexpected network access"))
            self.assertEqual(result["status"], "deferred_booking_running")
            self.assertFalse((root / "runtime/account-health.json").exists())

    def test_missing_and_invalid_sessions_never_trigger_login(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = self.fixture(root, ["UserId=1", "UserId=2"])
            (root / "account-01.json").unlink()
            (root / "account-02.json").write_text("[]")
            result = check(config, lambda *_: self.fail("Unexpected request"))
            self.assertEqual([r["status"] for r in result["results"]], ["session_missing", "session_file_invalid"])

    def test_http_status_classification_does_not_leak_details(self):
        for code, status in [(429,"rate_limited"),(503,"server_error"),(401,"login_required"),(403,"http_error")]:
            with patch("booking_tools.health.build_opener") as opener:
                opener.return_value.open.side_effect = HTTPError("secret", code, "secret", {}, None)
                self.assertEqual(request_status({"Cookie":"secret"}, "/User/CheckUserStatus"), (status, None))
        with patch("booking_tools.health.build_opener") as opener:
            opener.return_value.open.side_effect = URLError("private network detail")
            self.assertEqual(request_status({"Cookie":"secret"}, "/User/CheckUserStatus"), ("network_error", None))


if __name__ == "__main__":
    unittest.main()
