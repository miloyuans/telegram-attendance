# Telegram 工作打卡机器人（Go）

功能：

- 在指定 Telegram 群里监听关键词：默认 `上班`、`下班`、`1`
- 触发后发送带 3 个按钮的交互消息：`打卡上班`、`打卡下班`、`取消`
- 多用户隔离：按钮 callback data 绑定触发用户 ID，其他用户点击会被拒绝
- 点击打卡后按后端服务器当前时间记录，并反馈打卡时间
- 输入 `list` 后，为当前用户生成当月统计 HTML，并作为附件发送到群里
- 跨天夜班按“上班打卡所在日期”归属工时；当天只统计到昨天的工作打卡时间
- 如果昨天有上班但没有下班，会在今天同一用户记录里查找下班；仍找不到则标记异常：缺失下班记录
- 数据使用 JSONL 文件持久化，不依赖数据库

## 1. 创建 Bot

在 Telegram 找 `@BotFather`：

1. `/newbot` 创建机器人，拿到 `BOT_TOKEN`
2. 把机器人拉入目标群
3. 如果需要监听普通关键词，而不是 `/command`，需要在 BotFather 里使用 `/setprivacy` 关闭 Privacy Mode，或把 bot 设置成群管理员。否则机器人可能收不到群里的普通文本消息。

## 2. 获取群 chat_id

最简单方式：先不要配置 `ALLOWED_CHAT_IDS`，启动机器人后在目标群里发送 `上班`，机器人能响应后，再通过 Telegram 的更新日志或临时打印获得群 ID。

群/超级群 chat_id 通常是负数，例如：

```text
-1001234567890
```

## 3. 本地运行

```bash
export BOT_TOKEN="123456:ABC_xxx"
export ALLOWED_CHAT_IDS="-1001234567890"
export TIMEZONE="Asia/Shanghai"
export DATA_FILE="data/attendance.jsonl"
export REPORT_DIR="reports"

go run ./cmd/attendancebot
```

## 4. 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|---|---:|---|---|
| `BOT_TOKEN` | 是 | 无 | BotFather 发放的 Bot Token |
| `ALLOWED_CHAT_IDS` | 否 | 空 | 指定群 ID，多个用英文逗号分隔。为空时响应所有聊天，生产环境不建议为空 |
| `TIMEZONE` | 否 | `Asia/Shanghai` | 打卡时间和报表统计时区 |
| `DATA_FILE` | 否 | `data/attendance.jsonl` | 打卡数据文件 |
| `REPORT_DIR` | 否 | `reports` | HTML 报表输出目录 |
| `TRIGGER_KEYWORDS` | 否 | `上班,下班,1` | 触发按钮交互的关键词，精确匹配 |
| `LIST_KEYWORD` | 否 | `list` | 触发统计报表的关键词 |
| `SESSION_TTL` | 否 | `5m` | 每次按钮交互有效期 |

## 5. 构建

```bash
go build -o attendancebot ./cmd/attendancebot
./attendancebot
```

## 6. Docker 运行

```bash
docker build -t attendancebot .
docker run -d --name attendancebot \
  -e BOT_TOKEN="123456:ABC_xxx" \
  -e ALLOWED_CHAT_IDS="-1001234567890" \
  -e TIMEZONE="Asia/Shanghai" \
  -v "$PWD/data:/app/data" \
  -v "$PWD/reports:/app/reports" \
  attendancebot
```

## 7. 数据格式

每次打卡会向 `DATA_FILE` 追加一行 JSON：

```json
{"id":"...","chat_id":-1001234567890,"user_id":123,"username":"alice","full_name":"Alice","type":"in","time":"2026-06-10T21:00:00+08:00","created_at":"2026-06-10T13:00:00Z"}
```

`type` 值：

- `in`：上班打卡
- `out`：下班打卡

## 8. 跨天夜班统计逻辑

统计时以“上班打卡所在日期”为班次归属日期，并且 `list` 当天只统计到昨天的工作打卡时间。

每个上班记录的下班查找规则：

1. 先找同一用户、同一群、上班时间之后、当天内的下班记录。
2. 如果当天没有可配对的下班记录，再到次日查找同一用户的下班记录。
3. 如果次日仍找不到下班记录，该班次会进入 HTML 报表，并标记为：`异常：缺失下班记录`。该异常班次不计入总工时。

例如今天是 `2026-06-10`，系统最多计算 `2026-06-09` 的工作打卡时间：

```text
2026-06-09 22:00 上班
2026-06-10 07:00 下班
```

这条班次计入 `2026-06-09`，工时为 9 小时。

如果只有：

```text
2026-06-09 22:00 上班
```

但是在 `2026-06-10` 没找到该用户的下班打卡，报表会显示 `2026-06-09` 为 `异常：缺失下班记录`。

如果 `2026-06-01 06:00 下班` 对应的是 `2026-05-31 21:00 上班`，它属于 5 月 31 日，不会错误地跟 6 月 1 日晚上的上班记录配对。

## 9. 注意事项

- `1` 作为关键词非常宽泛，本项目默认做“精确匹配”，用户必须只发送 `1` 才会触发。
- JSONL 文件适合小团队使用；如果人数和数据量很大，可以把 `Store` 替换为 SQLite/MySQL。
- 机器人使用长轮询 `getUpdates`，启动时会调用 `deleteWebhook`，因此不要同时配置 webhook。
- Token 不要写进代码或提交到 Git 仓库。