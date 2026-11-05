"""Read-only session and venue checks. Never submits credentials or creates orders."""
import hashlib
import json
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener
from .session import ROOT, ORIGIN, NoRedirect, write_json

CN = timezone(timedelta(hours=8))


def request_status(session, endpoint):
    """Return a bounded response classification; never return headers or raw errors."""
    request = Request(ORIGIN + endpoint, headers={
        "Cookie": session["Cookie"], "User-Agent": session.get("UserAgent") or "OrderFieldSimulator/2",
        "X-Requested-With": "XMLHttpRequest", "Cache-Control": "no-cache, no-store"})
    try:
        with build_opener(NoRedirect()).open(request, timeout=8) as response:
            body = response.read(8193)
            if len(body) > 8192:
                return "unexpected_response", None
            return "ok", body.decode("utf-8-sig").strip()
    except HTTPError as error:
        if error.code == 429:
            return "rate_limited", None
        if error.code == 401:
            return "login_required", None
        if 300 <= error.code < 400 and "/user/login" in error.headers.get("Location", "").lower():
            return "login_required", None
        return ("server_error" if error.code >= 500 else "http_error"), None
    except (URLError, TimeoutError, OSError):
        return "network_error", None
    except (UnicodeError, ValueError):
        return "unexpected_response", None


def inspect_session(session, probe=request_status):
    status, body = probe(session, "/User/CheckUserStatus")
    if status != "ok":
        return status, None
    if body == "0":
        return "login_required", None
    if body != "1":
        return "unexpected_response", None
    status, body = probe(session, "/Field/GetOrderInfo?VenueNo=01")
    if status != "ok":
        return status, None
    try:
        limit = int(json.loads(body)["limitcount"])
        if limit < 1:
            raise ValueError()
    except (ValueError, TypeError, KeyError):
        return "unexpected_response", None
    return "ready", limit


def identity(session):
    """Same precedence as Go accountKey; used only in memory, never in reports."""
    cookies = dict(part.strip().split("=", 1) for part in session["Cookie"].split(";") if "=" in part)
    for name in ("UserId", "WXOpenId"):
        if cookies.get(name):
            return name + "=" + cookies[name]
    return session["Cookie"]


def check(config_path, probe=request_status, pause=time.sleep):
    config_path = Path(config_path).resolve()
    config = json.loads(config_path.read_text(encoding="utf-8"))
    root = config_path.parent
    state = root / config["StateDir"]
    # Do not add unrelated requests during a running booking attempt.
    if (state / "runner.lock").exists():
        return {"status": "deferred_booking_running", "reportUpdated": False}
    accounts = config.get("Accounts") or [{"Name": "primary", "CookieFile": config["CookieFile"]}]
    if len(accounts) > 10 or len({a["Name"] for a in accounts}) != len(accounts):
        raise ValueError("账号池超过上限或存在重复别名")
    checked_at = datetime.now(CN)
    results, usable, requests = [], {}, 0
    limited = False
    def counted(session, endpoint):
        nonlocal requests
        if requests:
            pause(1)
        requests += 1
        return probe(session, endpoint)
    for account in accounts:
        row = {"alias": account["Name"], "status": "not_checked"}
        results.append(row)
        if limited:
            row["status"] = "deferred_rate_limit"
            continue
        if (state / "runner.lock").exists():
            row["status"] = "deferred_booking_running"
            continue
        try:
            session = json.loads((root / account["CookieFile"]).read_text(encoding="utf-8"))
            if not isinstance(session, dict) or not isinstance(session.get("Cookie"), str) or not session["Cookie"] or any(c in session["Cookie"] for c in "\r\n"):
                raise ValueError()
            if not isinstance(session.get("UserAgent", ""), str) or any(c in session.get("UserAgent", "") for c in "\r\n"):
                raise ValueError()
        except FileNotFoundError:
            row["status"] = "session_missing"
            continue
        except (OSError, ValueError, TypeError):
            row["status"] = "session_file_invalid"
            continue
        row["status"], limit = inspect_session(session, counted)
        if limit is not None:
            row["selectionLimit"] = limit
        if row["status"] == "ready":
            key = identity(session)
            if key in usable:
                row["duplicateOf"] = usable[key]
            else:
                usable[key] = account["Name"]
        limited = row["status"] == "rate_limited"
    audit_path = state / "account-audit.json"
    excluded = []
    if audit_path.exists():
        audit = json.loads(audit_path.read_text(encoding="utf-8"))
        active = {a["Name"] for a in accounts}
        excluded = [{"alias": row["alias"], "lastAuthorizationStatus": row.get("status", "unknown")}
                    for row in audit.get("results", []) if row["alias"] not in active]
    signature = hashlib.sha256(json.dumps(results, sort_keys=True).encode()).hexdigest()
    previous_path = state / "account-health.json"
    previous = json.loads(previous_path.read_text(encoding="utf-8")) if previous_path.exists() else {}
    report = {"schemaVersion": 1, "checkedAt": checked_at.isoformat(), "status": "checked",
              "configuredSessions": len(accounts), "uniqueUsableIdentities": len(usable),
              "requests": requests, "results": results, "excludedNotAttempted": excluded,
              "signature": signature, "changed": signature != previous.get("signature"),
              "meaning": "ready仅表示当前会话与场馆查询通过，不证明有库存、无未支付限制或能下单。"}
    write_json(state / "account-health" / (checked_at.strftime("%Y%m%d-%H%M%S-%f") + ".json"), report)
    write_json(previous_path, report)
    return report


