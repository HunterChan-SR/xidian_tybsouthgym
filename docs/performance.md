# 预约链路性能记录

本轮优化针对已有串行预约链路，保留日期、同场两小时、预算、请求间隔、共享每日订单保护与手动支付要求。性能改善不等于竞抢成功率；真实测量与本地基准分开记录。

## 真实乒乓球前后对照：2026-11-08

用户授权同一已验证账号、2026-11-09 18:00–19:00、一小时最多6元、不支付，成功后核验并取消。前后各一次，均选中PPQ002、6元。前一笔取消确认后才开始后一轮；每轮一次创建、一次取消，最终均为 `test_cancelled`（取消流程确认 CheckStatus.PayStatus=4）。不进行重试采样或压力测试。

| 指标 | 优化前：文件oracle | 优化后：预热stdio | 说明 |
| --- | ---: | ---: | --- |
| oracle响应 | 722ms | 11ms | 优化前包含启动；优化后启动另计648ms，发生在库存查询前 |
| 库存候选观测→发出订单 | 804.205ms | 78.061ms | 选中候选后的关键等待减少约90% |
| 库存候选观测→收到下单结果 | 1537.205ms | 745.061ms | 本次减少约52%，也包含网络/服务器耗时 |
| 下单HTTP请求 | 733ms | 667ms | 小样本波动，不能归因为代码优化 |
| 全进程至取消结束 | 3108.823ms | 2813.603ms | 包含登录预检、worker启动、库存、下单、核验、取消 |
| 总HTTP请求数 | 10 | 10 | 请求范围与数量一致 |
| 下单次数 / 取消次数 | 1 / 1 | 1 / 1 | 两轮均未支付、无遗留测试订单 |

两轮发生于14:28与14:38，不是同一负载下的随机实验。它证明流程能完成，并给出这次观测到的耗时差；不能由两次成功推断账号/IP限速、服务端容差或明日成功率。生产轮次在放票前准备worker，因此关键指标是库存出现后的等待，而不是包含预热的进程总时长。

优化后下单分解：建立TCP连接1.044ms、TLS握手214.659ms、请求写出至首字节451.432ms；仅写出一次请求。`tlsResumed=false`，取消请求同样为false。客户端支持票据缓存，但这次真实平台没有恢复TLS会话；不能宣称此项已降低实盘耗时。后续瓶颈主要在网络/服务端，不能仅凭这个样本增加并发或缩短等待。

优化前构建ID：`59539a9355d643b4432a67dce51d62f7ecaaaa0c032e9099b294b20b840389f0`。

优化后构建ID：`d0906729a93272251eaa58de3e8854703cee61e4b3041b0a9bf505d44ade534c`。

原始事件、一次性尝试标记、订单与最终结果保存在本机 `runtime/live-tests/2026-11-08/performance-compare/{before,after}/`，不上传账号、订单标识或挑战数据。

## 完整链路审查与取舍

| 环节 | 检查结果与处理 | 可复核依据 |
| --- | --- | --- |
| 启动和放票等待 | 外部oracle每挑战冷启动占主要本地耗时；改为预检后、等待放票前启动一个worker，全池串行复用 | `oracle_worker.go`、启动ready与每次响应事件、进程生命周期测试 |
| 账号预检 | 同身份去重并保留有效会话；修复429后立即检查下一账号的问题，改为先停止遍历并遵守Retry-After有界等待 | `accounts_test.go`，含过期账号不重试与429不切换账号 |
| 日历与库存 | 保留GetWeek/dateadd核验、最新库存读取、时段优先级、价格与同场连续约束；不缓存竞争库存 | 日期/库存/周末回归测试；CPU基准见下 |
| 图片到答案 | 估计坐标与随机排除入口分开；阈值和既有算法不改。保留token、坐标范围及截止时间核验 | Python 14项oracle测试，真实参考坐标流程成功 |
| 进程间交接 | 可选stdio直接传递JSON，避免每次启动和临时文件持久化；原文件接口兼容 | 错配、越界、取消、EOF、超时、共享进程及零下单预演回归 |
| 查询连接 | 每账号使用可复用Transport与TLS票据缓存，结束时关闭空闲连接 | 模拟TLS测试，真实network事件；票据恢复的实盘收益尚未证实 |
| 下单连接 | 仍为新HTTP/1.1连接，禁止连接复用与HTTP/2重放路径；保留未知结果停止，不以“更快”为由盲重发GET | TLS1.2/1.3测试、丢响应只收到一次请求、跨账号未知结果保护回归 |
| 本地持久化 | 保留提交前原子写入与fsync，记录guardPersistMs；日志事件补足高精度时间 | 重启/未知订单保护测试、实际事件时间线 |
| 核验与取消 | 正规化未补零日期，核对日期/场地/时段/金额；取消后必须读取最终状态 | 日期与取消回归，两笔真实订单均完整取消 |
| 发布 | 同一构建入口跑Go测试/vet、Python测试；真实配置、日志和凭证留在本机，提交功能分支及PR | `main.py build`、GitHub Actions、构建清单 |

没有修改账号冷却、PollSeconds、提交并发或预约目标。保留原子订单保护的实际持久化成本；不通过异步省略保护来压缩耗时。选择算法的计算开销很小，不增加缓存与并行分支。

## 本地基准

两组已保存图片、每种方式20次：文件方式中位数929.423ms/P95 1018.097ms；预热管道往返7.524ms/P95 9.160ms，首条12.052ms。单次启动865.917ms另计。这组不访问平台，详见 [oracle接口](captcha-oracle.md)。

库存基准使用180条合成库存（12场×15小时），包含协议解析、候选选择及脱敏观测JSON生成。i5-13400 Windows本机结果约0.620ms/轮、263KB分配/轮；不是平台库存查询的网络耗时。

```powershell
python -X utf8 -m booking_tools.benchmark_oracle --samples evidence/captcha-samples/2026-11-08 --output runtime/oracle-benchmark.json --repeats 20
go test ./cmd/booking -run '^$' -bench BenchmarkInventoryPipeline -benchmem -benchtime=1s
python -X utf8 main.py build
```

## 观测字段与连接依据

HTTP事件新增network字段：dnsMs、connectMs、tlsMs、tlsResumed、connectionReused、writeMs、writeAttempts、ttfbMs和responseWaitMs；仅在相应阶段发生时出现。ttfbMs是请求开始到首字节，已包含连接/握手等阶段，不能再次与这些字段相加。responseWaitMs从请求写出到首字节，包含网络与服务器时间，不能当作纯服务端计算耗时。事件不保存地址、完整URL、请求头、Cookie或TLS票据。

连接处理参考Go标准库的 [Transport重试与连接规则](https://pkg.go.dev/net/http#Transport)、[TLS ClientSessionCache](https://pkg.go.dev/crypto/tls#Config) 和 [httptrace阶段回调](https://pkg.go.dev/net/http/httptrace#ClientTrace)，并以仓库所用Go版本及模拟丢包测试验证行为。
