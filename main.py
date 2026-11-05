"""Single maintenance entry point; booking.exe remains the reservation runner."""
import argparse
import json
import re
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from booking_tools.session import ROOT


def account_alias(value):
    if not re.fullmatch(r"primary|account-(?:0[1-9]|[1-9][0-9])", value):
        raise argparse.ArgumentTypeError("使用 primary 或 account-01 至 account-99")
    return value


def main(argv=None):
    parser = argparse.ArgumentParser(description="西电预约：登录、检查、构建和复盘")
    actions = parser.add_subparsers(dest="action", required=True)
    login = actions.add_parser("login", help="打开本机官方登录窗口")
    login.add_argument("--account", type=account_alias, default="primary")
    health = actions.add_parser("health", help="只读检查已配置账号，不登录或下单")
    health.add_argument("--config", type=Path, default=ROOT / "booking.json")
    actions.add_parser("check", help="离线测试和静态检查")
    actions.add_parser("build", help="检查通过后构建预约程序")
    today = datetime.now(timezone(timedelta(hours=8))).date().isoformat()
    for name, help_text in (("prepare", "冻结当日预约计划"), ("review", "生成诊断和研究复盘")):
        command = actions.add_parser(name, help=help_text)
        command.add_argument("--date", type=date.fromisoformat, default=today)
        if name == "prepare":
            command.add_argument("--revise", action="store_true")
    refresh = actions.add_parser("refresh", help="仅对明确获准的旧账号做一次登录刷新")
    refresh.add_argument("--indices", required=True, help="已获准账号的旧配置序号，如2或2,4")
    refresh.add_argument("--auto-slider", action="store_true")
    captcha = actions.add_parser("captcha", help="显示官方下单滑块，供用户人工完成")
    captcha.add_argument("--request", required=True, type=Path)
    args = parser.parse_args(argv)
    if args.action == "captcha":
        from booking_tools.captcha import run
        return run(args.request)
    if args.action == "login":
        from booking_tools.manual import login as run_login
        return run_login(args.account)
    if args.action == "health":
        from booking_tools.health import check
        result = check(args.config)
    elif args.action in {"check", "build"}:
        from booking_tools.maintain import run_quality
        return run_quality(args.action)
    elif args.action in {"prepare", "review"}:
        from booking_tools.research import prepare, review
        day = args.date.isoformat()
        result = prepare(ROOT, day, args.revise) if args.action == "prepare" else review(ROOT, day)
    else:
        from booking_tools.batch import refresh_accounts
        return refresh_accounts(args.indices, args.auto_slider)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
