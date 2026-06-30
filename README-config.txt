使用方法：

1. 把 config.example.json 复制成 config.json
2. 把 bot_token 改成 BotFather 给你的 token
3. 运行：go run .
4. 初次测试 allowed_chat_ids 保持 []，表示允许所有群，方便先拿到真实 chat_id
5. 终端日志里看到真实 chat_id 后，可以再填入 allowed_chat_ids；为空允许所有群，非空只允许白名单群使用打卡、报表、取消等功能

v20 核心变化：

1. 修复交互按钮模式最终结果仍出现激励语和固定标语的问题。
2. 交互按钮模式现在只输出打卡结果和时间，例如：

   Milo Mi 💻 Check in completed ✅
   Time: 2026-06-30 13:00:07

3. 激励语和默认固定标语只保留在 simple 简化关键字模式。

v19 核心变化：

1. slist 全员汇总现在会同时发送两个附件：HTML + XLSX。HTML 使用和 XLSX 一致的表格结构，方便直接在浏览器查看。
2. interactive / all 模式下，list 和 slist 报告默认使用英文；simple 模式下保持中文。
3. 交互按钮打卡完成后的最终结果通知已去除激励语和固定标语，只保留打卡结果与时间。
4. 新增 manual_command_dedupe_seconds，默认 20 秒，避免同一用户短时间内重复触发 slist 导致重复附件。
5. 仍然建议同一个 Bot Token 只运行一个实例；如果多实例运行，请确保共享同一个 state_file/lock_file，避免 Telegram 更新被多个实例重复处理。

v17 核心变化：

1. 交互弹窗入口改为独立配置：
   - interactive_trigger_keywords 默认 ["/start", "start"]
   - 只做消息开头匹配，兼容大小写
   - 示例：/start、Start、start now、/start@bot 都会触发
   - 示例：hello start、please /start、普通聊天里包含 start 不会触发
   - 旧 interactive_keywords 只保留为兼容字段，新交互弹窗入口统一使用 interactive_trigger_keywords

2. 新增用户级交互关闭：
   - interaction_exit_keywords 默认 ["exit", "quit", "close", "clos", "closure"]
   - 用户直接发送这些完整字符串时，只关闭该用户在当前群里的交互会话，不影响其他用户
   - 机器人只清理自己发出的交互按钮消息，不删除用户的 /start 或 exit/quit/close 消息，避免普通群权限下出现 deleteMessage 报错

3. 交互按钮新增取消入口：
   - ❌ Cancel 按钮会立即关闭本次交互
   - 取消时只清理交互窗口，不写入打卡记录

4. 交互完成后自动清理：
   - 用户点击 Check in / Check out / Go to break / Come from break 后，会删除机器人发出的交互按钮消息
   - 最后只保留一条最终英文结果通知
   - 交互模式的按钮、提示、回调提示、最终结果通知均使用英文
   - v19 起交互最终通知不再追加激励语和固定标语

5. 代码保持 v16 的多文件结构：
   - config.go：配置、环境变量解析、模式解析
   - telegram.go：Telegram Bot API 封装
   - store.go：打卡记录存储和兼容旧数据读取
   - reports.go：工时统计、HTML 报表、XLSX 汇总
   - bot.go：机器人主流程、消息、按钮、定时月报
   - main.go：程序入口

打卡模式：

- default_attendance_mode 默认 interactive
- chat_attendance_modes 可按群 ID 覆盖
- 支持值：interactive / simple / all

三种模式说明：

- interactive：默认模式。只有 interactive_trigger_keywords 中配置的开头字符串才会弹出英文按钮菜单；上班/下班/休息等普通关键词不会直接落库，也不会弹窗。
- simple：简化关键字模式。用户发送上班/下班/休息/回来工作等完整关键词时直接记录，不弹按钮。
- all：全模式。关键词直接记录；interactive_trigger_keywords 命中时弹出按钮菜单。

英文交互按钮：

- 💻 Check in
- 🛌 Check out
- ☕ Go to break
- ↩️ Come from break
- ❌ Cancel

休息时间扣除逻辑：

- 用户 Check in 后到 Check out 前，Go to break 到 Come from break 之间的时间不计入工作工时。
- 如果 Go to break 后没有 Come from break，但随后 Check out，则从 Go to break 到 Check out 的时间也会扣除。
- 报表里会显示总时长、休息扣除、计入工时。
- 月度总工时只累加“计入工时”。

群数据隔离：

- 新数据默认写入 data/chats/chat_<chat_id>/attendance.jsonl
- 每个群自动独立目录，避免不同群数据混合
- 个人 HTML 和全员 XLSX 报表默认写入 reports/chats/chat_<chat_id>/
- data_file 保留为旧版单文件兼容读取路径；新数据不再写入旧 data_file

常用配置：

allowed_chat_ids：群白名单。
- [] 表示所有群都可以使用。
- 配置后，只有列表里的群 ID 可以使用。

打卡模式：
- default_attendance_mode：默认 interactive。
- chat_attendance_modes：按群 ID 覆盖模式，例如：
  {
    "-1001111111111": "interactive",
    "-1002222222222": "simple",
    "-1003333333333": "all"
  }

关键词说明：
- interactive_trigger_keywords：交互弹窗启动词，默认 /start / start，只做开头匹配。
- interaction_exit_keywords：交互退出词，默认 exit / quit / close / clos / closure，只做完整匹配。
- clock_in_keywords：上班关键词，默认包含 上班 / 打卡上班 / check in。
- clock_out_keywords：下班关键词，默认包含 下班 / 打卡下班 / check out。
- break_start_keywords：离开工作/休息关键词，默认包含 休息 / 离开工作 / go to break / break / start break。
- break_end_keywords：返回工作关键词，默认包含 回来工作 / 回到工作 / 结束休息 / come from break / back to work / end break。
- trigger_keywords：旧版兼容配置，不再作为交互弹窗主入口。
- cancel_keywords：取消打卡入口，默认包含 取消 / 取消打卡 / 取消上班 / 取消下班 / 取消休息。

功能边界：
- 直接打卡关键词仍然只做完整匹配，避免“今天不上班”“今天休息”等句子误触发。
- 交互弹窗只看消息开头，不做包含匹配，避免普通聊天误触发。
- simple 模式下，/start 这类交互启动词不会弹按钮；只响应配置的直接打卡关键词。

报表说明：
- list：生成个人当月 HTML 打卡日历。
- slist：生成当前月全员汇总，并同时发送 HTML + XLSX 两个附件。
- 自动月度汇总：默认每月 1 号 15:00 后发送上月完整全员 HTML + XLSX 工时统计表。
- 自动月度汇总防重复状态保存在 state_file 的 monthly_summary_sent 中。
- monthly_summary_chat_ids 为空时，优先使用 allowed_chat_ids；如果 allowed_chat_ids 也为空，会从群数据目录和旧 data_file 中推断群 ID。

取消打卡说明：
- 取消 / 取消打卡：列出发起用户当天全部记录。
- 取消上班：只列出当天 Check in 记录。
- 取消下班：只列出当天 Check out 记录。
- 取消休息：列出当天休息相关记录。
- 取消操作会同时尝试删除新群目录数据和旧 data_file 里的匹配记录。

激励语说明：
- motivation_enabled：是否在完成通知里追加激励语，默认 true。
- motivation_remote_enabled：是否启用互联网实时抓取，默认 true。
- v19 起交互模式最终通知不再追加激励语和固定标语，只保留打卡结果和时间。
- 简化关键字模式保持原来的中/日/英随机激励语逻辑。

环境变量覆盖：
BOT_TOKEN=xxx
ALLOWED_CHAT_IDS=-1001234567890,-1009876543210
TIMEZONE=Asia/Dubai
DATA_DIR=data/chats
DATA_FILE=data/attendance.jsonl
REPORTS_DIR=reports
STATE_FILE=data/bot_state.json
LOCK_FILE=data/bot.lock
DEFAULT_ATTENDANCE_MODE=interactive
CHAT_ATTENDANCE_MODES=-1001111111111:interactive,-1002222222222:simple,-1003333333333:all
INTERACTIVE_TRIGGER_KEYWORDS=/start,start
INTERACTION_EXIT_KEYWORDS=exit,quit,close,clos,closure
SUMMARY_KEEP_MONTHS=3
MANUAL_COMMAND_DEDUPE_SECONDS=20
MONTHLY_SUMMARY_ENABLED=true
MONTHLY_SUMMARY_DAY=1
MONTHLY_SUMMARY_HOUR=15
MONTHLY_SUMMARY_MINUTE=0
MONTHLY_SUMMARY_CHAT_IDS=-1001234567890,-1009876543210
DEBUG=true
CONFIG_FILE=config.json
