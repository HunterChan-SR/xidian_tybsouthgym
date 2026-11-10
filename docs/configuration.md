# 配置

从 booking.example.json 复制为 booking.json。真实配置不进入 Git；示例默认 AllowLive=false。

| 字段 | 含义 |
| --- | --- |
| VenueNo / FieldTypeNo | 场馆与运动类型；南羽毛球01/001，北普通羽毛球01/007 |
| DateAdd | 目标日期相对当天的天数 |
| BeginTime / EndTime | 基础时段，示例19:00–21:00 |
| PreferredFields / AllowOtherFields / FieldPrefix | 优先场地、是否允许其他场及类型前缀 |
| MaxTotalCents | 整笔订单金额上限，整数分 |
| ReleaseTime / WindowSeconds | 当天释放时间与执行窗口 |
| PollSeconds / RequestTimeoutSeconds | 有界查询间隔与请求超时 |
| Accounts | 已授权本机会话路径与账号别名，最多10项 |
| CookieFile | 单账号默认会话路径 |
| StateDir / PoolID | 状态目录及跨账号共享的目标订单保护标识 |
| AllowLive | 是否允许真实预约；新配置默认为false |
| ParallelOrders / ParallelStaggerMillis / MaxTemporaryCents | 默认串行；2启用两路，初始错峰150ms、临时上限4800分，每笔仍≤2400分，仅原南校区目标。见[流水线](pipeline.md) |
| OrderTLSPreconnect | 默认false；释放前750ms为已验证身份各预握手一次，连接最多保留5秒并仅承载一次请求，HTTP代理沿用原CONNECT隧道，其他代理回退原连接方式；见[流水线](pipeline.md) |
| BookingCaptcha | 获取下单滑块并等待答案；当前平台应开启 |
| CaptchaSolverCommand | 可选的本机求解命令及参数数组，空数组或省略时使用官方人工滑块窗口；详见[oracle 接口](captcha-oracle.md) |
| CaptchaSolverPersistent | 默认false；true要求命令支持stdio协议，在放票前预热一个进程、全池串行复用，本轮结束回收；不适用于默认Edge窗口 |
| WeekendAfternoon | 南校区目标日期为周六或周日时，15–17优先、19–21备选；要求基础时段19–21且最多24元 |
| EveningFlexible / AllDayFallback | 北校区晚间择优与全天备选选项，不用于南校区 |

金额和日期最终按官方订单明细核验。未知响应停止提交，不通过关闭保护或更换 PoolID 重试。
