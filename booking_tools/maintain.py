"""Offline quality gate and traced build. All tests use local mocks, never live orders."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from datetime import datetime, timezone, timedelta
from booking_tools.build import digest, source_fingerprint
from booking_tools.session import ROOT, find_tool, write_json



def run(args, env):
    subprocess.run([str(arg) for arg in args], cwd=ROOT, env=env, check=True)


def release_freeze_active(root, now=None):
    """Do not replace the production executable during final preparation/window."""
    path = root / "booking.json"
    if not path.exists():
        return False
    config = json.loads(path.read_text(encoding="utf-8"))
    now = now or datetime.now(timezone(timedelta(hours=8)))
    now = now.astimezone(timezone(timedelta(hours=8)))
    release = datetime.combine(now.date(), datetime.strptime(config["ReleaseTime"], "%H:%M:%S").time(), now.tzinfo)
    return release - timedelta(minutes=15) <= now <= release + timedelta(seconds=config["WindowSeconds"] + 10)


def check_build_window(root):
    if release_freeze_active(root):
        raise RuntimeError("已进入放票前15分钟的版本冻结期；不替换预约程序。使用已验证版本或停止该轮，窗口后再修改。")


def run_quality(action):
    if action not in {"check", "build"}:
        raise ValueError("未知维护操作")
    go = find_tool("go/bin/" + ("go.exe" if os.name == "nt" else "go"))
    if not go:
        raise RuntimeError("请安装 go.mod 所需 Go 并加入 PATH，或放入项目旁的 OrderFieldSimulator-tools/go")
    if (ROOT / "runtime/runner.lock").exists():
        raise RuntimeError("存在预约运行锁，先检查现有进程；不替换运行程序")
    if action == "build":
        check_build_window(ROOT)
    env = dict(os.environ, GOTOOLCHAIN="local", GOWORK="off", PYTHONUTF8="1")
    source_hash, files = source_fingerprint(ROOT)
    validation_path = ROOT / "runtime/local-validation.json"
    write_json(validation_path, {"at": datetime.now(timezone.utc).isoformat(),
        "passed": False, "status": "running", "sourceSHA256": source_hash, "action": action})
    try:
        gofmt = Path(go).with_name("gofmt.exe" if os.name == "nt" else "gofmt")
        formatted = subprocess.check_output([str(gofmt), "-l", *[str(ROOT / p) for p in files if p.endswith(".go")]], env=env, text=True)
        if formatted.strip():
            raise ValueError("请先格式化 Go 源码：\n" + formatted)
        run([go, "test", "./...", "-count=1"], env)
        run([go, "vet", "./..."], env)
        run([sys.executable, "-X", "utf8", "-m", "unittest", "discover", "-s", "tests", "-v"], env)
        if source_fingerprint(ROOT)[0] != source_hash:
            raise RuntimeError("检查期间源码变化，请重新运行")
        if action == "build":
            temporary = ROOT / "runtime/booking-candidate.exe"
            temporary.parent.mkdir(parents=True, exist_ok=True)
            run([go, "build", "-trimpath", "-ldflags", "-X main.buildID=" + source_hash, "-o", temporary, "./cmd/booking"], env)
            if (ROOT / "runtime/runner.lock").exists() or source_fingerprint(ROOT)[0] != source_hash:
                raise RuntimeError("构建期间开始预约或源码变化，保留当前程序")
            check_build_window(ROOT)
            target = ROOT / "booking.exe"
            if target.exists():
                rollback = ROOT / "runtime/rollback"
                rollback.mkdir(parents=True, exist_ok=True)
                shutil.copy2(target, rollback / "booking.exe")
                manifest_path = ROOT / "runtime/build-manifest.json"
                if manifest_path.exists():
                    shutil.copy2(manifest_path, rollback / "build-manifest.json")
            os.replace(temporary, target)
            write_json(ROOT / "runtime/build-manifest.json", {
                "schemaVersion": 3, "builtAt": datetime.now(timezone.utc).isoformat(),
                "buildID": source_hash, "sourceSHA256": source_hash, "sourceFiles": files,
                "binarySHA256": digest(target),
                "goVersion": subprocess.check_output([go, "version"], text=True, env=env).strip(),
                "validation": ["gofmt", "go test ./... -count=1", "go vet ./...", "python unittest discover"]})
        write_json(ROOT / "runtime/local-validation.json", {
            "at": datetime.now(timezone.utc).isoformat(), "passed": True, "status": "passed", "sourceSHA256": source_hash,
            "action": action, "checks": ["gofmt", "go test", "go vet", "python unittest"]})
    except BaseException as error:
        write_json(validation_path, {"at": datetime.now(timezone.utc).isoformat(),
            "passed": False, "status": "failed", "sourceSHA256": source_hash,
            "action": action, "errorType": type(error).__name__})
        raise
    print("离线检查通过" + ("，程序及构建记录已更新。" if action == "build" else "。"))
    return 0

