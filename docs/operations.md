# 日常操作

## 准备

先检查真实 booking 进程、runtime/runner.lock 及目标日期订单保护。已有进程只跟踪结果，不能重复启动或删除保护；锁遗留时先核对对应进程和提交日志。结果不明时用同账号核对官方订单，不能凭单次未找到就重发。

本机生产目标以 booking.json 为准。示例为南校区01/001、两天后、YMQ004优先、最多24元，全池每天一笔，用户手动支付。WeekendAfternoon=true 时目标日期为周六或周日才使用15–17优先、19–21备选；由同一进程按库存选择，不能同时启动两个时段的预约进程。

## 会话与下单验证码

python -X utf8 main.py login --account primary 用于手动登录；已有授权账号可使用其本机别名。密码、Cookie和验证码不得写入聊天、公开日志或 Git。

health 顺序检查 CheckUserStatus 和场馆查询；ready 只证明查询可用。区分会话失效、网络故障、HTTP限流、服务器异常和未知响应。429停止后续账号检查，不通过换账号规避等待。

BookingCaptcha=true 时，程序选择候选后获取官方 GetFieldSliderCaptcha 挑战，将 SliderToken/SliderX 只附加到接下来的一次提交。它独立于校园登录验证码。未配置oracle时在Edge官方组件中等待用户拖动，预约会话由Go持有。可运行 `.\booking.exe -config booking.json -mode captcha-preview` 预演当前配置的人工窗口或oracle；结果写入 `runtime/latest-captcha-preview.json`，流程在下单之前结束。

配置 CaptchaSolverCommand 后，人工求解交接由指定的本机命令承担，预演和下单使用同一接口。命令协议、坐标单位、截止时间和返回格式见 [oracle 接口](captcha-oracle.md)。答案准备完成时记录 booking_captcha_ready 及求解进程耗时，平台是否接受答案由实际下单响应确定。

支持stdio的命令可配合 CaptchaSolverPersistent=true，在放票前加载Python依赖、全池串行复用同一worker，本轮结束即回收。启动耗时单独记录为 booking_oracle_started；每个新挑战仍独立处理。既有算法估计坐标入口为 `main.py oracle-reference`；`oracle-random` 保留随机排除测试语义，正式使用估计坐标时不得选错命令。切换由booking.json两个字段明确决定，不由构建动作隐式决定。

取消、超时、协议变化或验证码拒绝均停止本次流程；滑块挑战和人工结果仅在 runtime/captcha 临时存放并在正常结束时清理，不进入日志或仓库。程序被强制终止可能留下临时文件，确认无运行进程后可清理这些挑战文件，不能因此删除订单保护。

## 每日流程

1. 11:00，无预约进程和未解决锁时，顺序运行 main.py health --config booking.json 与 main.py check；核对本轮报告和构建来源。
2. 正常修改11:40前完成，11:45至12:01:40版本冻结。冻结期间不编译、替换程序或改运行中的配置。
3. 11:55复核会话、配置和订单保护，main.py prepare --date 当天日期冻结计划；有依据修改时使用 --revise 保留旧计划。
4. 在放票前10分钟内运行 booking.exe -config booking.json -mode book 一次，等待12点，保持运行至真实退出。超过12:01:30不新启动。
5. 有候选时通过当前配置的人工窗口或oracle处理新挑战。成功后核验订单日期、实际场地、时段、金额和支付期限，再到官方平台手动支付。
6. main.py review --date 当天日期生成报告；填写 decision.json，区分事实、解释、反例和混杂因素。只读探测与真实竞争分别统计。

## 本机工具和资料

Go 及 Edge 驱动可放在项目旁 OrderFieldSimulator-tools/go/bin、OrderFieldSimulator-tools/edge。入口优先查找相邻工具，再使用 PATH；驱动版本应与 Edge 主版本匹配。

旧材料保存在项目外归档或被忽略的本机目录。Git 仓库不包含 data/、runtime/、booking.json、evidence/、实际研究计划及历史观测。首次配置复制示例，不覆盖已有本机会话和订单保护。
