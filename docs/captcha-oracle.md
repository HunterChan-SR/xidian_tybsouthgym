# 本机 oracle 接口

预约程序获取官方挑战后，将图片交给本机 oracle，由其返回横向位置。Go 负责会话、挑战关联、下单及订单保护。官方人工拖动窗口是默认实现，也可接入本机适配程序；本仓库提供既有算法估计坐标及随机排除测试两个独立入口。

## 配置

在本机配置中开启 BookingCaptcha，并给出命令及独立参数。

```json
{
  "BookingCaptcha": true,
  "CaptchaSolverCommand": ["python", "-X", "utf8", "C:/booking/human_oracle.py"]
}
```

human_oracle.py 是你自己的适配程序路径。空数组或省略 CaptchaSolverCommand 使用内置 Edge 人工窗口。命令直接执行，工作目录是 booking.exe 所在目录，脚本路径建议使用绝对路径。默认文件协议在命令末尾附加两个参数 `--request` 和请求文件的绝对路径。

## 预热串行 worker

对于支持标准输入输出协议的 oracle，可设置 `CaptchaSolverPersistent=true`。下面的可选配置返回既有算法估计坐标：

```json
{
  "BookingCaptcha": true,
  "CaptchaSolverCommand": ["python", "-X", "utf8", "main.py", "oracle-reference"],
  "CaptchaSolverPersistent": true
}
```

Go 在账号预检之后、等待放票之前启动一个 worker，等待其完成依赖加载。所有账号依次复用这个进程；挑战仍在选中候选后才获取，每次携带新的账号别名、token 和截止时间。进程结束本轮后回收，不作为后台常驻服务。只读库存模式不启动 worker。

此模式自动向命令追加 `--stdio`，不创建挑战/答案临时文件，协议如下：

1. worker 加载依赖后输出一行 `{"ready":true}` 并 flush。
2. Go 向 stdin 写入一行请求 JSON，字段与下方文件协议一致。
3. worker 向 stdout 返回一行答案 JSON 并 flush；Go 核对当前 token、坐标和截止时间。
4. 在下一条请求到来前不再输出内容。一次只能处理一个请求；stdin EOF 表示结束。

stdout 专用于协议，不能夹杂日志。错误、取消或超时会停止本轮，Go 不自动重启 worker 或重发挑战。每次交接仍最长90秒，服从预约窗口截止时间。启动完成记录 `booking_oracle_started.elapsedMs`；每次交接记录 `booking_captcha_ready.elapsedMs` 和 `transport=stdio`，分别衡量启动与响应耗时。无需改动现有文件适配器；不设置本开关就沿用原方式。

### 两个明确的坐标入口

| 命令 | 返回值 | 函数 |
| --- | --- | --- |
| `oracle-reference` | 既有算法估计的原图坐标 | `generate_reference_reply` |
| `oracle-random --exclusion-radius 10` | 排除参考坐标邻域后的随机坐标 | `generate_random_reply` |

两者均支持 `--request 路径` 或 `--stdio`，图片处理、算法阈值保持一致。11月8日优化前，本机 `oracle-random` 曾被改成直接返回 `reference_x`；该行为现在有独立的 `oracle-reference` 入口，随机入口恢复其测试语义。需要保持当时的估计坐标用途，应将命令改为 `oracle-reference`。`booking.oracle.example.json` 仍使用随机模式、AllowLive=false，并开启预热。正式配置由使用者在本机明确选择，不覆盖已有目标、预算及订单保护。

### 离线性能复测

```powershell
python -X utf8 -m booking_tools.benchmark_oracle --samples evidence/captcha-samples/2026-11-08 --output runtime/oracle-benchmark.json --repeats 20
```

只读取本地样例、启动本机子进程，不访问平台。2026-11-08两组保存图片、reference模式各20次：文件子进程往返中位数929.423ms、P95 1018.097ms；预热后管道往返中位数7.524ms、P95 9.160ms，首条12.052ms。单次启动865.917ms单独记录，由放票前等待期承担。计时包含 Python 处理与答案交接；文件方式不包含调用方创建请求文件的时间，二者均不含平台网络请求。报告位于本机 `runtime/maintenance/2026-11-08/oracle-performance-after.json`。这些是本机样例测量，不能推导实盘成功率。

## 请求与答案

请求文件是 UTF-8 JSON，位于配置的 StateDir/captcha，每次调用使用独立路径。

```json
{
  "account": "account-02",
  "deadline": "2026-11-08T12:01:30+08:00",
  "challenge": {
    "type": 1,
    "token": "当前挑战标识",
    "bg": "服务端返回的JPEG图片base64",
    "piece": "服务端返回的PNG图片base64",
    "y": 40
  }
}
```

challenge 原样保留服务端字段。oracle 可以展示图片供人工判断，或调用已有的人工计算接口。请求包含账号别名和挑战数据；账号 Cookie 由 Go 客户端持有。

文件模式在请求路径后追加 `.reply` 的文件中写入答案，然后以退出码 0 结束；stdio模式直接输出相同JSON结构。

```json
{"token": "回传本次challenge.token", "x": 123}
```

`x` 是拼图左边缘在原图坐标系中的横向位置，单位为逻辑像素，整数范围为 `0 <= x <= bg宽度 - piece宽度`，含两端。宽度取解码后完整图片的宽度，包含拼图透明边缘。例如背景宽300、拼图宽50时，范围为0–250。Go根据挑战图片独立计算这个上限，校验人工窗口或外部oracle的返回值。官方组件使用 `round(显示位移 / 图片缩放比例)` 计算此值。图片缩放后，需要把人工测得的位移换算回原图坐标。token 从请求中原样回传，用于核对答案对应的挑战。

用户取消时写入 `{"cancelled": true}`。错误退出、缺失或无效答案、错配 token、超时均停止当前预约流程。每次调用最长90秒，同时服从上层运行截止时间，请求中的 deadline 是本地执行截止时间。服务器挑战有效期仍需通过实际响应确认。

外部命令的 stdout 和 stderr 不转发到预约日志。oracle 应将账号标识和挑战数据限制在本机交接用途，并在进程结束前关闭自身子进程。Go 在文件模式调用结束后清理请求和答案文件。

## 最小适配示意

下面的 solve 表示已有的人工接口。实现者负责展示挑战、等待人工答案并遵守 deadline。

```python
import argparse
import json
from pathlib import Path
from your_human_service import solve

parser = argparse.ArgumentParser()
parser.add_argument('--request', required=True, type=Path)
args = parser.parse_args()
request = json.loads(args.request.read_text(encoding='utf-8'))
x = solve(request['challenge'], deadline=request['deadline'])
reply = {'cancelled': True} if x is None else {
    'token': request['challenge']['token'], 'x': x,
}
Path(str(args.request) + '.reply').write_text(
    json.dumps(reply, ensure_ascii=False), encoding='utf-8')
```

## 验证与调度

仓库提供随机坐标测试实现 `booking_tools/oracle.py`，入口为 `python -X utf8 main.py oracle-random --request 请求路径 --exclusion-radius 10`。每次调用先检查本地截止时间，将bg、piece严格Base64解码，再使用已有OpenCV依赖解析图片并保留透明通道。

`generate_random_reply(background, piece, *, token, exclusion_radius=10)` 接收两张已解码的NumPy图片数组及当前token，完成以下流程：

1. 检查图片非空、拼图宽高不超过背景，排除半径为非负整数。
2. 将图片无损编码为PNG，适配现有 `batch.slider_distance` 的Base64入参；使用原图宽度作为rendered_width，得到原图参考x。求解算法及阈值沿用现有实现。
3. 从合法范围 `[0, bg宽度-piece宽度]` 排除闭区间 `[参考x-半径, 参考x+半径]`；区间超出图片时裁到合法范围。
4. 对剩余整数位置等概率采样，返回完整的 `{'token': token, 'x': x}`。通过一次随机编号映射选择，不使用反复抽样循环。

例如背景宽300、拼图宽50、参考x=94、半径10时，排除 `[84,104]`，剩余 `[0,83]` 和 `[105,250]` 共230个位置。默认半径10是离线测试参数，尚无平台容差证据；0表示只排除参考坐标。求解失败、参考位置越界或无剩余位置时返回失败，不退回全范围随机；等宽图片也不再生成原先唯一的x=0。函数不读写文件；CLI负责原子写入答案文件并退出。图片缺失、非法Base64、损坏图片、尺寸不匹配及过期请求同样返回非零退出码。

可以单独调用这个函数测试图片到reply的交接，后续人工接口可参照同样的参数和返回格式：

```python
from booking_tools.oracle import decode_challenge_image, generate_random_reply

background = decode_challenge_image(challenge['bg'], 'bg')
piece = decode_challenge_image(challenge['piece'], 'piece')
reply = generate_random_reply(background, piece, token=challenge['token'], exclusion_radius=10)
```

两组2026-11-08真实平台图片样例保存在本机 `evidence/captcha-samples/2026-11-08/challenge-01.json` 和 `challenge-02.json`。每组包含原始Base64的bg、piece及采集时间、尺寸信息，没有会话凭证或真实token；该目录由Git忽略。离线开发可读取样例的challenge字段，再传入自定的 `token='offline-sample'` 调用上述函数，无需请求平台。仓库回归测试使用生成的合成图片，运行不依赖本机样例。

样例的 `referenceAnswer` 保存现有 `booking_tools.batch.slider_distance` 算法计算的离线参考x、源码摘要、调用宽度及匹配诊断。两组参考x均为94；第一组在给定y处有94、95两个并列峰值，保留算法返回的94。`serverVerified=false` 表示这些值没有经过平台验题，匹配分数也不是正确率。以原图宽度作为rendered_width，可令函数直接返回原图坐标：

```python
from booking_tools.batch import slider_distance

reference_x = slider_distance(challenge['bg'], challenge['piece'], background.shape[1])
```

这些标注用于离线结果对照，oracle直接根据输入图片重新求解，不读取预存标注。由于平台容差未知，排除参考位置的邻近区间也不能保证真实随机提交必定失败；不创建订单的流程验证使用 `captcha-preview`。

### 容差调查记录（2026-11-08）

当天重新读取官方 `Views/js/sliderCaptcha.js` 和 `Views/Field/FieldOrder.html`：组件将token与原图x交给onVerify回调，页面随即把SliderToken、SliderX连同预约参数发往OrderField或OrderFieldFree。所检查的公开流程没有独立验题请求，也没有公开服务器容差值。页面副本保存在本机 `evidence/captcha-tolerance/2026-11-08/`。这项观察不等于证明平台不存在其他测试接口。

当前服务器容差仍为未知。图像匹配的并列峰值只表示算法的不确定性，不能当作服务器接受区间。要用排除区间确保失败，至少需要已知的算法定位误差上界和服务器容差上界；即使假设接受区间连续且对称，所需排除半径也要覆盖两者之和。有限次拒绝、某个旧挑战的区间或本地模拟阈值均不能证明这个上界。

用户先完成了保存样例上的离线集成，随后明确授权真实边界试验：account-02，2026-11-09 18:00–19:00任一空闲乒乓球台，最多6元，不支付。固定测试参考x右侧，偏移10到0逐像素收缩，最多11次、间隔至少60秒；只在明确坐标拒绝时继续，首笔成功后停止探测并核验取消，其他异常或结果不明停止。本轮授权已执行完毕，不能重复启动。

实际提交了3次，间隔均超过60秒，均使用新挑战：

| 参考x | 右侧偏移 | 提交x | 实际反馈 |
| --- | --- | --- | --- |
| 135 | +10 | 145 | 明确坐标错误 |
| 101 | +9 | 110 | 明确坐标错误 |
| 218 | +8 | 226 | 创建成功，随后核验并取消 |

订单为PPQ002、11月9日18–19点、6元，未支付，最终CheckStatus.PayStatus=4。自动核验曾因服务端日期 `2026/11/9` 未补零而中断；对实际日期、场地、时段、金额补充核验后完成取消，并修复日期解析、增加取消流程回归。证据保存在本机 `runtime/live-tests/2026-11-08/boundary-oracle/`，保留各次尝试保护和原始核验失败记录。

这只能说明本轮算法参考值右侧+10、+9分别拒绝，+8在另一个挑战上创建成功；挑战和算法误差不同，尚不能证明统一8像素容差、左侧范围或完整接受区间。随机oracle仍按显式测试半径（默认10）排除，再对剩余位置均匀采样，不据此缩小半径或将其视为零订单保证。失败分支测试使用本地模拟服务，真实挑战交接使用captcha-preview。

`booking.oracle.example.json` 已配置随机oracle和 `--exclusion-radius 10`，默认 AllowLive=false。填入已有授权会话的本机路径后，可执行预演。

```powershell
.\booking.exe -config booking.oracle.example.json -mode captcha-preview
```

随机答案用于验证接口交互。预演成功表示程序接受了答案格式与挑战关联，真实平台对答案的判定仍发生在下单请求中。离线账号池测试会启动这个实际Python组件，由模拟服务分别返回场地被抢和成功；另有验证码拒绝场景。模拟服务的成功响应不能用于评估随机答案的真实通过率。

配置好实际 oracle 后，运行 `.\booking.exe -config booking.json -mode captcha-preview`。预演获取官方挑战、调用 oracle、检查返回格式及关联，结果写入 runtime/latest-captcha-preview.json。预演成功表示答案已经准备好，服务端验题发生在实际下单请求中。

当前调度按账号顺序获取各自挑战并提交。平台明确返回场地被抢后，按既有间隔刷新候选并继续账号池；成功或结果不明则停止全池。每次新尝试获取新的挑战。提前储备答案、跨账号复用答案和并发提交尚未由本接口启用。

离线测试使用合成图片字段、本机模拟服务器及模拟 oracle 子进程，覆盖答案提交、账号接替、取消、超时、无效答案、清理及预演零下单。
