"""Validate the explicitly selected legacy accounts, once per account."""
import base64
import json
import math
import time
from urllib.parse import urlparse

from booking_tools.session import ROOT, HOST, ORIGIN, TARGET, find_tool, valid_session, write_json

def slider_distance(background_data, piece_data, rendered_width):
    import cv2
    import numpy as np
    background_color = cv2.imdecode(np.frombuffer(base64.b64decode(background_data), dtype=np.uint8), cv2.IMREAD_COLOR)
    piece_rgba = cv2.imdecode(np.frombuffer(base64.b64decode(piece_data), dtype=np.uint8), cv2.IMREAD_UNCHANGED)
    background = cv2.imdecode(np.frombuffer(base64.b64decode(background_data), dtype=np.uint8), cv2.IMREAD_GRAYSCALE)
    piece = cv2.imdecode(np.frombuffer(base64.b64decode(piece_data), dtype=np.uint8), cv2.IMREAD_GRAYSCALE)
    if background is None or piece is None or piece.shape[0] > background.shape[0] or piece.shape[1] >= background.shape[1]:
        raise ValueError("captcha_image_shape_changed")
    if piece_rgba is not None and piece_rgba.ndim == 3 and piece_rgba.shape[2] == 4:
        contour = cv2.Canny(piece_rgba[:, :, 3], 50, 100)
        count = np.count_nonzero(contour)
        if count >= 20:
            white = (np.min(background_color, axis=2) > 215).astype(np.uint8) * 255
            white = cv2.dilate(white, np.ones((3, 3), np.uint8))
            overlap = cv2.matchTemplate(white.astype(np.float32)/255, contour.astype(np.float32)/255, cv2.TM_CCORR) / count
            _, score, _, point = cv2.minMaxLoc(overlap)
            other = overlap.copy()
            other[:, max(0, point[0]-10):point[0]+11] = -1
            distance = round(point[0] * rendered_width / background.shape[1])
            if score >= 0.90 and score - float(other.max()) >= 0.25 and 5 < distance < rendered_width - 40:
                return distance
    edges = cv2.Canny(background, 150, 250)
    template = cv2.Canny(piece, 150, 250)
    if np.count_nonzero(template) < 20:
        raise ValueError("captcha_image_ambiguous")
    scores = cv2.matchTemplate(edges, template, cv2.TM_CCOEFF_NORMED)
    _, confidence, _, location = cv2.minMaxLoc(scores)
    x = location[0]
    masked = scores.copy()
    masked[:, max(0, x-10):x+11] = -1
    second = float(masked.max())
    check = cv2.matchTemplate(cv2.Canny(background, 100, 200), cv2.Canny(piece, 100, 200), cv2.TM_CCOEFF_NORMED)
    corroborated_x = cv2.minMaxLoc(check)[3][0]
    if confidence < 0.30 or confidence - second < 0.08 or abs(corroborated_x - x) > 2:
        raise ValueError("captcha_image_ambiguous")
    distance = round(x * rendered_width / background.shape[1])
    if not 5 < distance < rendered_width - 40:
        raise ValueError("captcha_distance_out_of_range")
    return distance

def drag_once(driver):
    from selenium.webdriver.common.by import By
    from selenium.webdriver.common.action_chains import ActionChains
    background = driver.find_element(By.ID, "slider-img1").get_attribute("src")
    piece = driver.find_element(By.ID, "slider-img2").get_attribute("src")
    if not background.startswith("data:image/") or not piece.startswith("data:image/"):
        raise ValueError("captcha_not_ready")
    for name, source in (("background", background), ("piece", piece)):
        (ROOT / "runtime" / ("captcha-" + name + ".png")).write_bytes(base64.b64decode(source.split(",", 1)[1]))
    width = driver.find_element(By.ID, "sliderDiv").rect["width"]
    distance = slider_distance(background.split(",", 1)[1], piece.split(",", 1)[1], width)
    slider = driver.find_element(By.CSS_SELECTOR, "#sliderDiv .slider")
    action = ActionChains(driver).click_and_hold(slider).pause(0.1)
    previous = 0
    for step in range(1, 21):
        position = round(distance * (1-math.cos(math.pi*step/20))/2)
        delta = position - previous
        if delta:
            action.move_by_offset(delta, 0).pause(0.04)
        previous = position
    action.release().perform()

def inspect_account(username, password, alias, auto_slider):
    from selenium import webdriver
    from selenium.webdriver.common.by import By
    from selenium.webdriver.support.ui import WebDriverWait
    from selenium.webdriver.support import expected_conditions as EC
    from selenium.webdriver.edge.service import Service
    from selenium.common.exceptions import WebDriverException
    driver = None
    captcha_attempted = False
    try:
        options = webdriver.EdgeOptions()
        options.add_argument("--inprivate")
        driver_path = find_tool("edge/msedgedriver.exe")
        service = Service(driver_path) if driver_path else Service()
        driver = webdriver.Edge(options=options, service=service)
        driver.set_page_load_timeout(30)
        # Follow the current official campus entry instead of a hard-coded old CAS chain.
        driver.get(ORIGIN + "/User/UserChoose?LoginType=1")
        wait = WebDriverWait(driver, 20)
        user = wait.until(lambda d: next((e for e in d.find_elements(By.CSS_SELECTOR, ".m-account #username") if e.is_displayed()), False))
        user.click()
        wait.until(lambda d: not user.get_attribute("readonly"))
        user.clear()
        user.send_keys(username)
        secret = driver.find_element(By.CSS_SELECTOR, ".m-account #password")
        secret.click()
        wait.until(lambda d: not secret.get_attribute("readonly"))
        secret.clear()
        secret.send_keys(password)
        driver.execute_script("window.captchaAudit = []; $(document).ajaxComplete(function(e,x,s){if(s.url.indexOf('verifySliderCaptcha.htl')>=0){try {window.captchaAudit.push(JSON.parse(x.responseText).errorCode)} catch(e){window.captchaAudit.push('invalid_response')}}});")
        next(e for e in driver.find_elements(By.ID, "login_submit") if e.is_displayed()).click()
        deadline = time.monotonic() + (40 if auto_slider else 300)
        while time.monotonic() < deadline:
            time.sleep(1)
            current = urlparse(driver.current_url)
            if auto_slider and current.hostname == "ids.xidian.edu.cn" and "/reAuthCheck/" in current.path:
                return {"alias": alias, "status": "additional_verification_required", "captchaAttempted": captcha_attempted}
            if current.hostname == HOST:
                cookie = "; ".join(c["name"]+"="+c["value"] for c in driver.get_cookies()
                                   if c.get("value") and
                                   (c.get("domain", "").lstrip(".") == HOST or
                                    HOST.endswith("."+c.get("domain", "").lstrip("."))))
                ua = driver.execute_script("return navigator.userAgent")
                if cookie and valid_session(cookie, ua):
                    relative = "data/accounts/" + alias + ".json"
                    write_json(ROOT / relative, {"Cookie": cookie, "UserAgent": ua})
                    return {"alias": alias, "status": "valid", "cookieFile": relative,
                            "captchaAttempted": captcha_attempted}
            errors = driver.find_elements(By.ID, "showErrorTip")
            message = " ".join(e.text for e in errors if e.is_displayed())
            if any(x in message for x in ("密码有误", "密码错误", "用户名或密码", "用户不存在", "账号被锁", "帐号被锁", "已停用")):
                return {"alias": alias, "status": "credentials_or_account_rejected"}
            captcha = driver.find_elements(By.ID, "captcha-id")
            if captcha and captcha[0].is_displayed():
                if not auto_slider:
                    continue
                if not captcha_attempted:
                    captcha_attempted = True
                    try:
                        drag_once(driver)
                    except ValueError as error:
                        return {"alias": alias, "status": str(error)}
        return {"alias": alias, "status": "captcha_or_login_not_completed", "captchaAttempted": captcha_attempted,
                "captchaCodes": driver.execute_script("return window.captchaAudit || []"),
                "message": message.replace(username, "[account]").replace(password, "[secret]")[:150],
                "finalHost": urlparse(driver.current_url).hostname, "finalPath": urlparse(driver.current_url).path}
    except WebDriverException as error:
        return {"alias": alias, "status": "browser_or_selector_error", "errorClass": type(error).__name__}
    finally:
        if driver is not None:
            try:
                driver.quit()
            except WebDriverException:
                pass

def refresh_accounts(selected_indices, auto_slider=False):
    from cryptography.fernet import Fernet
    indices = list(dict.fromkeys(int(v) for v in selected_indices.split(",")))
    encrypted = json.loads((ROOT / "data/user_password.json").read_text(encoding="utf-8"))["users"]
    if any(i < 1 or i > len(encrypted) for i in indices):
        raise ValueError("账号序号越界")
    cipher = Fernet((ROOT / "data/secret.key").read_bytes())
    report_path = ROOT / "runtime/account-audit.json"
    previous = json.loads(report_path.read_text(encoding="utf-8")) if report_path.exists() else {}
    results = previous.get("results", [])
    write_json(report_path, {"results": results, "complete": False, "selected": indices})
    for index in indices:
        alias = f"account-{index:02d}"
        user = encrypted[index-1]
        try:
            password = cipher.decrypt(user["password"].encode()).decode()
            result = inspect_account(str(user["username"]), password, alias, auto_slider)
            del password
        except Exception as error:
            result = {"alias": alias, "status": "local_error", "errorClass": type(error).__name__}
        result["checkedAt"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
        results = [r for r in results if r["alias"] != alias] + [result]
        write_json(ROOT / "runtime/account-audit.json", {"results": results, "complete": False})
        print(json.dumps(result, ensure_ascii=False), flush=True)
        time.sleep(3)
    # Append only validated accounts; retain the previously authenticated primary account.
    config_path = ROOT / "booking.json"
    config = json.loads(config_path.read_text(encoding="utf-8"))
    accounts = config.get("Accounts") or [{"Name": "primary", "CookieFile": config["CookieFile"]}]
    by_alias = {a["Name"]: a for a in accounts}
    for result in results:
        if result["status"] == "valid":
            by_alias[result["alias"]] = {"Name": result["alias"], "CookieFile": result["cookieFile"]}
    # Identity deduplication happens in the Go preflight, not by username disclosure.
    config["Accounts"] = list(by_alias.values())
    config["PoolID"] = "badminton-19-21"
    if (ROOT / config["StateDir"] / "runner.lock").exists():
        print("预约程序正在运行，已保存会话，暂不修改账号池。", flush=True)
    else:
        write_json(config_path, config)
    write_json(ROOT / "runtime/account-audit.json", {"results": results, "complete": True})

    return 0
