"""Interactive authorization, isolated from booking and health checks."""
import json
import time
from urllib.parse import quote, urlparse
from .session import ROOT, ORIGIN, HOST, TARGET, find_tool, write_json, valid_session

def login(account="primary"):
    from selenium import webdriver
    from selenium.common.exceptions import WebDriverException
    status = ROOT / "runtime" / "login-status.json"
    write_json(status, {"status": "opening"})
    driver = None
    try:
        options = webdriver.EdgeOptions()
        options.add_argument("--inprivate")
        from selenium.webdriver.edge.service import Service
        driver_path = find_tool("edge/msedgedriver.exe")
        service = Service(driver_path) if driver_path else Service()
        driver = webdriver.Edge(options=options, service=service)
        driver.set_page_load_timeout(30)
        driver.get(ORIGIN + "/User/Login?BackUrl=" + quote(TARGET, safe=""))
        write_json(status, {"status": "waiting_for_manual_login"})
        print("请在新打开的 Edge 窗口自行完成校园登录及验证码。成功后会保存本机会话并关闭此窗口。", flush=True)
        deadline = time.monotonic() + 900
        while time.monotonic() < deadline:
            time.sleep(5)
            if urlparse(driver.current_url).hostname != HOST:
                continue
            cookies = driver.get_cookies()
            cookie = "; ".join(
                c["name"] + "=" + c["value"] for c in cookies
                if c.get("value") and
                (c.get("domain", "").lstrip(".") == HOST or
                 HOST.endswith("." + c.get("domain", "").lstrip(".")))
            )
            ua = driver.execute_script("return navigator.userAgent")
            if cookie and valid_session(cookie, ua):
                relative = "data/session.json" if account == "primary" else "data/accounts/" + account + ".json"
                write_json(ROOT / relative, {"Cookie": cookie, "UserAgent": ua})
                if account != "primary":
                    config_path = ROOT / "booking.json"
                    config = json.loads(config_path.read_text(encoding="utf-8"))
                    if (ROOT / config["StateDir"] / "runner.lock").exists():
                        print("会话已保存；预约程序正在运行，暂不修改账号池。", flush=True)
                    else:
                        accounts = config.get("Accounts") or [{"Name": "primary", "CookieFile": config["CookieFile"]}]
                        accounts = [a for a in accounts if a["Name"] != account]
                        if len(accounts) >= 10:
                            print("会话已保存，但账号池已达10个配置上限；请先整理重复或不再使用的条目。", flush=True)
                            write_json(status, {"status": "saved_not_registered", "reason": "account_pool_full"})
                            return 1
                        accounts.append({"Name": account, "CookieFile": relative})
                        config["Accounts"] = accounts
                        config["PoolID"] = "badminton-19-21"
                        write_json(config_path, config)
                audit_path = ROOT / "runtime/account-audit.json"
                if account != "primary":
                    audit = json.loads(audit_path.read_text(encoding="utf-8")) if audit_path.exists() else {"results": [], "complete": True}
                    audit["results"] = [r for r in audit["results"] if r["alias"] != account] + [{"alias": account, "status": "valid", "cookieFile": relative, "method": "manual", "checkedAt": time.strftime("%Y-%m-%dT%H:%M:%S%z")}]
                    write_json(audit_path, audit)
                write_json(status, {"status": "saved"})
                print("登录已核验，会话已保存。未创建订单。", flush=True)
                return 0
        write_json(status, {"status": "timed_out"})
        print("等待登录超时，请重新运行登录入口。", flush=True)
        return 1
    except WebDriverException as exc:
        write_json(status, {"status": "browser_error", "errorType": type(exc).__name__})
        print("浏览器启动或连接失败。请检查 Edge 与 Selenium 驱动，或重新打开登录入口。", flush=True)
        return 1
    finally:
        if driver is not None:
            try:
                driver.quit()
            except WebDriverException:
                pass
