"""Fingerprint all maintained Go/Python source, tests and dependency manifests."""
import hashlib
import json
from pathlib import Path

EXCLUDED = {".tools", ".git", ".venv", "data", "runtime", "__pycache__"}


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def source_files(root):
    root = Path(root)
    paths = [p for p in root.rglob("*") if p.suffix in {".go", ".py"}
             and not any(part in EXCLUDED for part in p.relative_to(root).parts)]
    paths += [root / name for name in ("go.mod", "go.sum", "requirements.txt") if (root / name).exists()]
    return {p.relative_to(root).as_posix(): digest(p) for p in sorted(paths)}


def source_fingerprint(root):
    files = source_files(root)
    encoded = json.dumps(files, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest(), files


def verified_build_id(root, manifest):
    if digest(root / "booking.exe") != manifest["binarySHA256"]:
        raise ValueError("程序与构建记录不一致，先核对构建和测试")
    if manifest.get("schemaVersion") != 3:
        raise ValueError("构建记录需覆盖当前Go/Python源码，请运行 python main.py build")
    fingerprint, files = source_fingerprint(root)
    if fingerprint != manifest["sourceSHA256"] or files != manifest["sourceFiles"]:
        raise ValueError("源码集合或内容与构建记录不一致，请重新检查并构建")
    return fingerprint
