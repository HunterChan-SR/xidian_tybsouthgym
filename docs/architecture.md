# 架构与可验证边界

## 设计选择

Go核心集中在cmd/booking/，保留一个可执行程序包，按职责拆为多个文件；不为目录层级引入多余接口或循环依赖。纯数据转换、平台协议、状态持久化、调度分别阅读和测试。

| 模块 | 职责 | 对应验证 |
|---|---|---|
| main.go | 参数、会话装配、结果输出 | 调用下方模块，不放竞争策略 |
| config.go | 严格配置解析、路径归一化、合法性验证 | config_test.go |
| domain.go | 数据结构、金额与时间解析 | domain_test.go |
| client.go | HTTP边界、会话/场馆检查、请求记录 | client_test.go |
| accounts.go | 身份去重、有界预检、刷新会话接替 | accounts_test.go |
| inventory.go | 日期映射、库存解析、同场连续选择 | inventory_test.go |
| orders.go | 订单保护、提交、订单核验、获准测试的取消逻辑 | orders_test.go |
| captcha.go / booking_tools/captcha.py | 获取本次下单挑战，由用户在官方组件完成滑块；同会话、单次提交、取消和超时停止 | captcha_test.go |
| state.go | 原子写入与互斥锁 | state_test.go |
| scheduler.go | 放票时间、轮转、退避、停止条件 | scheduler_test.go |
| telemetry.go | 脱敏事件和目标库存证据 | telemetry_test.go |
| booking_tools/session.py | 本机通用会话工具、原子JSON写入 | 登录与健康检查共用 |
| booking_tools/manual.py | 人工登录、保存授权 | 实际登录需用户在本机完成 |
| booking_tools/health.py | 独立只读健康检查与历史报告 | tests/test_health.py |
| booking_tools/build.py | 完整Go源码集合指纹 | tests/test_build.py |
| booking_tools/research.py | 离线计划、请求诊断与复盘 | tests/test_research.py、tests/test_cli.py |
| booking_tools/maintain.py / build.py | 统一检查、构建及源码指纹 | tests/test_build.py |

main.py统一维护命令并按需加载模块，帮助与离线操作不会启动浏览器。booking_tools/batch.py仅用于明确选定的旧账号刷新。旧登录、付款、生成器源码已移至项目外本机归档；当前没有根目录兼容脚本或重复入口。

## 预约状态约束

    配置/会话预检 → 等待窗口 → 读取库存 → 选择候选
                                           ↓
                                     人工完成下单验证码 → 持久化pending保护
                                           ↓
                                         提交一次
                   ┌───────────────────────┼─────────────────────┐
                明确拒绝                 返回成功               结果不明
                   ↓                       ↓                     ↓
           记录拒绝/释放本次保护         核对实际订单           保留保护/停止全池
                   ↓                       ↓
           已知可重试原因才退避          停止全池/通知支付

关键不变量：

1. 一个PoolID和目标日期共享一份订单保护，所有账号与重启后都遵守。
2. 发请求前写pending；超时、未知响应、崩溃不允许自动重新提交。
3. 下单接口虽然使用GET，也不能当作可重放查询；使用独立连接避免透明重放。
4. 金额用整数分；候选必须完整覆盖目标连续时段。
5. DateBeginTime只是当天时间模板；目标使用GetWeek+dateadd，最终用订单OrderDate核验。
6. 账号去重在有效预检之后完成，失效旧会话不遮挡同身份的新会话。
7. 健康检查不下单、不改配置；网络错误、限流和未知响应不会记为账号失效。

## 版本与证据

main.py build对Go/Python源码、测试与依赖清单建立路径和内容摘要，并将buildID嵌入预约程序。清单保存各文件摘要和二进制摘要；main.py prepare同时验证当前源码、程序和最近离线检查，任何维护源码变化都会使旧构建失效。旧清单只作为历史证据，不再用于验证新布局。

main.py review同时生成请求诊断和研究复盘，共用原子JSON写入，不覆盖人工决定。检查开始即使旧通过报告失效，失败留下明确的failed状态。测试覆盖旧回放、新命令入口、模块迁移及检查失败保护。

这用于来源追溯，不是字节完全可复现构建声明。跨天负载不可控，离线测试、只读检查、测试订单和真实竞争样本仍须分别解释。
