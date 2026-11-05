"""Display the official booking slider for a human; never solves or submits orders."""
import json
import time
from pathlib import Path
from urllib.parse import urlparse
from .session import ORIGIN, HOST, find_tool, write_json


def run(request_path):
    from selenium import webdriver
    from selenium.webdriver.edge.service import Service
    path = Path(request_path).resolve()
    data = json.loads(path.read_text(encoding='utf-8'))
    driver = None
    try:
        options = webdriver.EdgeOptions()
        options.add_argument('--inprivate')
        tool = find_tool('edge/msedgedriver.exe')
        driver = webdriver.Edge(options=options, service=Service(tool) if tool else Service())
        driver.set_page_load_timeout(20)
        driver.get(ORIGIN + '/favicon.ico')
        for part in data['cookie'].split(';'):
            if '=' in part:
                name, value = part.strip().split('=', 1)
                driver.add_cookie({'name': name, 'value': value, 'path': '/', 'secure': True})
        driver.get(ORIGIN + '/Views/Field/FieldOrder.html?VenueNo=01&FieldTypeNo=' + data['fieldType'])
        if urlparse(driver.current_url).hostname != HOST:
            return 1
        # Render the challenge issued by the Go client's cookie jar in the official widget.
        # No automated pointer movement; onVerify only records the user's gesture locally.
        driver.execute_script('''
          const challenge=arguments[0];
          const original=$.ajax;
          $.ajax=function(options){
            if(typeof options==='object' && String(options.url).includes('GetFieldSliderCaptcha')){
              options.success(challenge); return;
            }
            return original.apply(this,arguments);
          };
          FieldSliderCaptcha.show({url:'../../Field/GetFieldSliderCaptcha',
            onVerify:function(token,x,ctrl){window.bookingHumanReply={token:token,x:x};ctrl.close();},
            onCancel:function(){window.bookingHumanReply={cancelled:true};}});
          document.title='请完成预约滑块 - '+arguments[1];
        ''', data['challenge'], data['account'])
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            reply = driver.execute_script('return window.bookingHumanReply || null')
            if reply is not None:
                write_json(Path(str(path) + '.reply'), reply)
                return 0
            time.sleep(.2)
        return 1
    except Exception:
        return 1
    finally:
        if driver is not None:
            try:
                driver.quit()
            except Exception:
                pass
