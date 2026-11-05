"""Shared project paths, atomic state writes and read-only session validation."""
import json
import os
import shutil
import tempfile
from pathlib import Path
from urllib.request import Request, build_opener, HTTPRedirectHandler
from urllib.error import HTTPError, URLError

ROOT = Path(__file__).resolve().parents[1]
ORIGIN = "https://tybsouthgym.xidian.edu.cn"
HOST = "tybsouthgym.xidian.edu.cn"
TARGET = ORIGIN + "/Views/Field/FieldOrder.html?VenueNo=01&FieldTypeNo=001"

def find_tool(relative):
    """Use sibling portable tools, then a system installation on PATH."""
    relative = Path(relative)
    external = ROOT.parent / "OrderFieldSimulator-tools" / relative
    return str(external) if external.is_file() else shutil.which(relative.name)

def write_json(path, value):
    """Atomic replacement with a unique temporary file; no partial JSON on interruption."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent,
                                         prefix=".state-", suffix=".tmp", delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)

class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

def valid_session(cookie, ua):
    request = Request(ORIGIN + "/User/CheckUserStatus", headers={
        "Cookie": cookie, "User-Agent": ua, "X-Requested-With": "XMLHttpRequest"
    })
    try:
        with build_opener(NoRedirect()).open(request, timeout=8) as response:
            return response.status == 200 and response.read(1024).strip() == b"1"
    except (HTTPError, URLError, TimeoutError):
        return False

