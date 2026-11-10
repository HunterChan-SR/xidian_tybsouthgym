# 西电场地预约助手

用于西电体育场馆平台的授权预约、会话检查与运行复盘。Go 执行库存选择和预约，Python 提供登录、人工验证码窗口及维护命令。付款由用户在官方平台完成。

## 功能与边界

- 南校区羽毛球同场连续两小时，优先场地及金额上限可配置。
- 周末优先15:00–17:00，无匹配组合时选择19:00–21:00；工作日使用配置时段。
- 已授权身份去重；默认串行，可配置获准的两路错峰预约。成功或未知停止新派发并收齐在途结果，多余未支付单取消核验后最终保留一笔。
- 下单前通过官方组件由用户手动完成滑块，也可配置[本机 oracle 接口](docs/captcha-oracle.md)。支持文件交接及放票前预热的串行worker，程序校验答案与当前挑战的关联后提交。
- 可选的放票前 TLS 提前握手及零下单预演；[三账号流水线方案与离线模型](docs/pipeline.md)。
- 只读健康检查、离线测试、构建来源记录及逐日复盘。
- 北校区普通场适配仍保留在代码中，需单独配置范围与预算。

平台接口会变化，程序不保证预约成功。运行期间电脑需保持联网；使用默认人工窗口时，用户需能看到并操作窗口。可选oracle的配置和本地/实测性能见[性能记录](docs/performance.md)。

## 环境

Windows、Python 3.11+、Go 1.24.1+、Microsoft Edge。Go 核心使用标准库；Python 依赖见 requirements.txt。Go 和浏览器驱动可在 PATH 中安装，也可放在项目旁的 OrderFieldSimulator-tools/go 与 OrderFieldSimulator-tools/edge，详见 [日常操作](docs/operations.md)。

```powershell
python -m pip install -r requirements.txt
Copy-Item booking.example.json booking.json
Copy-Item research/plan.example.json research/plan.json
Set-Content research/hypotheses.json '[]' -Encoding utf8
python -X utf8 main.py build
python -X utf8 main.py login --account primary
python -X utf8 main.py health --config booking.json
```

示例默认 AllowLive=false。确认自己的预约范围、账号授权、预算与放票时间后，再修改本机 booking.json 开启提交。凭证只在本机保存，不写入源码。

## 日常使用

```powershell
python -X utf8 main.py health --config booking.json
python -X utf8 main.py check
python -X utf8 main.py prepare --date YYYY-MM-DD
.\booking.exe -config booking.json -mode book
python -X utf8 main.py review --date YYYY-MM-DD
```

预约程序只在配置的释放窗口内工作。默认在选择候选后弹出官方滑块；配置oracle后按指定接口处理挑战，预热worker在等待放票前启动。取消、超时或验证失败会停止本次提交。不要在进程运行时修改配置。

main.py login 打开官方登录页面；refresh --indices 仅供本机已有、明确授权的旧账号刷新。main.py captcha 是预约程序调用的内部人工验证入口。probe 只查询库存，不创建订单。

## 结构

| 路径 | 职责 |
| --- | --- |
| cmd/booking/ | Go 协议、库存、调度、订单保护与测试 |
| booking_tools/ | Python 登录、人工验证、检查及复盘 |
| tests/ | Python 离线测试 |
| docs/ | 配置、架构与操作说明 |
| research/ | 可复制的观测计划示例 |
| .github/workflows/ | 离线检查 CI |
| data/、runtime/、booking.json | 本机授权及运行数据，Git 忽略 |

## 开发与发布

```powershell
python -X utf8 main.py check
python -X utf8 main.py build
```

测试使用合成数据和本地模拟服务，不需要真实账号。build 在全部检查通过后更新 booking.exe 与本机构建清单。

阅读 [贡献说明](CONTRIBUTING.md)、[敏感数据说明](SECURITY.md)、[架构](docs/architecture.md) 和 [配置](docs/configuration.md)。第三方页面快照、旧账号资料、真实观测、订单记录及可执行文件不进入仓库。

本项目尚未选择开源许可证；整理为 Git/GitHub 仓库不代表已经授予开源使用许可。正式公开发布前应由维护者明确原项目及新增代码的权利归属和许可。
