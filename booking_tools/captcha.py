"""Display an official booking slider for manual interaction."""
import json
import time
from pathlib import Path
from urllib.parse import urlparse

from .session import HOST, ORIGIN, find_tool, write_json


LOAD_WIDGET = '''
const done = arguments[0];
document.body.replaceChildren();
const load = src => new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = src;
    script.onload = resolve;
    script.onerror = reject;
    document.head.appendChild(script);
});
load('/Views/js/jqurey.js')
    .then(() => load('/Views/js/sliderCaptcha.js'))
    .then(() => done(typeof window.FieldSliderCaptcha?.show === 'function'))
    .catch(() => done(false));
'''


SHOW_WIDGET = '''
const challenge = arguments[0];
const original = $.ajax;
$.ajax = function (options) {
    if (typeof options === 'object' && options.url === '/Field/GetFieldSliderCaptcha') {
        options.success(challenge);
        return;
    }
    return original.apply(this, arguments);
};
FieldSliderCaptcha.show({
    url: '/Field/GetFieldSliderCaptcha',
    onVerify: function (token, x, ctrl) {
        window.bookingHumanReply = {token: token, x: x};
        ctrl.close();
    },
    onCancel: function () { window.bookingHumanReply = {cancelled: true}; }
});
document.title = '请完成预约滑块 - ' + arguments[1];
'''


WIDGET_READY = '''
const mask = document.getElementById('fieldSliderCaptchaMask');
if (!mask || !mask.getClientRects().length) return false;
const handle = mask.querySelector('[data-role=handle]');
const images = [...mask.querySelectorAll('[data-role=bg], [data-role=piece]')];
return handle?.getBoundingClientRect().width > 0 && images.length === 2 &&
    images.every(img => img.complete && img.naturalWidth > 0);
'''


def run(request_path):
    from selenium import webdriver
    from selenium.webdriver.edge.service import Service
    from selenium.webdriver.support.ui import WebDriverWait

    path = Path(request_path).resolve()
    driver = None
    stage = 'request'
    try:
        data = json.loads(path.read_text(encoding='utf-8'))
        stage = 'browser_start'
        options = webdriver.EdgeOptions()
        options.add_argument('--inprivate')
        tool = find_tool('edge/msedgedriver.exe')
        driver = webdriver.Edge(options=options, service=Service(tool) if tool else Service())
        driver.set_page_load_timeout(20)
        driver.set_script_timeout(10)
        stage = 'origin_page'
        driver.get(ORIGIN + '/favicon.ico')
        parsed = urlparse(driver.current_url)
        if parsed.scheme != 'https' or parsed.hostname != HOST:
            raise RuntimeError('unexpected origin')
        stage = 'widget_load'
        if not driver.execute_async_script(LOAD_WIDGET):
            raise RuntimeError('widget unavailable')
        stage = 'widget_render'
        driver.execute_script(SHOW_WIDGET, data['challenge'], data['account'])
        WebDriverWait(driver, 8).until(lambda d: d.execute_script(WIDGET_READY))
        print('booking_captcha_visible', flush=True)
        stage = 'human_interaction'
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            reply = driver.execute_script('return window.bookingHumanReply || null')
            if reply is not None:
                write_json(Path(str(path) + '.reply'), reply)
                return 0
            time.sleep(.2)
        print('booking_captcha_timeout', flush=True)
        return 1
    except Exception as exc:
        # Never print exception messages or challenge contents.
        print(f'booking_captcha_failure {stage} {type(exc).__name__}', flush=True)
        return 1
    finally:
        if driver is not None:
            try:
                driver.quit()
            except Exception:
                pass
