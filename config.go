package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const version = "2026-06-30-attendance-v24-delete-trigger-message"

// =========================
// 配置
// =========================

type Config struct {
	BotToken                   string            `json:"bot_token"`
	AllowedChatIDs             []int64           `json:"allowed_chat_ids"`
	Timezone                   string            `json:"timezone"`
	DataDir                    string            `json:"data_dir"`
	DataFile                   string            `json:"data_file"` // legacy single-file data path, still read for compatibility
	ReportsDir                 string            `json:"reports_dir"`
	StateFile                  string            `json:"state_file"`
	LockFile                   string            `json:"lock_file"`
	Debug                      bool              `json:"debug"`
	TriggerKeywords            []string          `json:"trigger_keywords"`
	ClockInKeywords            []string          `json:"clock_in_keywords"`
	ClockOutKeywords           []string          `json:"clock_out_keywords"`
	BreakStartKeywords         []string          `json:"break_start_keywords"`
	BreakEndKeywords           []string          `json:"break_end_keywords"`
	InteractiveKeywords        []string          `json:"interactive_keywords"` // legacy field, no longer used as popup trigger
	InteractiveTriggerKeywords []string          `json:"interactive_trigger_keywords"`
	InteractionExitKeywords    []string          `json:"interaction_exit_keywords"`
	CancelKeywords             []string          `json:"cancel_keywords"`
	DefaultAttendanceMode      string            `json:"default_attendance_mode"`
	ChatAttendanceModes        map[string]string `json:"chat_attendance_modes"`
	ListKeyword                string            `json:"list_keyword"`
	SummaryKeyword             string            `json:"summary_keyword"`
	PollTimeoutSeconds         int               `json:"poll_timeout_seconds"`
	SummaryKeepMonths          int               `json:"summary_keep_months"`
	MonthlySummaryEnabled      bool              `json:"monthly_summary_enabled"`
	MonthlySummaryDay          int               `json:"monthly_summary_day"`
	MonthlySummaryHour         int               `json:"monthly_summary_hour"`
	MonthlySummaryMinute       int               `json:"monthly_summary_minute"`
	MonthlySummaryChatIDs      []int64           `json:"monthly_summary_chat_ids"`
	MotivationEnabled          bool              `json:"motivation_enabled"`
	MotivationRemoteEnabled    bool              `json:"motivation_remote_enabled"`
	MotivationCandidateCount   int               `json:"motivation_candidate_count"`
	MotivationTimeoutSeconds   int               `json:"motivation_timeout_seconds"`
	MotivationLogFailures      bool              `json:"motivation_log_failures"`
	ManualCommandDedupeSeconds int               `json:"manual_command_dedupe_seconds"`
	ReportMultiFormatEnabled   bool              `json:"report_multi_format_enabled"`
	UserAliases                map[string]string `json:"user_aliases"`
}

func defaultConfig() Config {
	return Config{
		Timezone:                   "Asia/Dubai",
		DataDir:                    "data/chats",
		DataFile:                   "data/attendance.jsonl",
		ReportsDir:                 "reports",
		StateFile:                  "data/bot_state.json",
		LockFile:                   "data/bot.lock",
		Debug:                      false,
		TriggerKeywords:            []string{"1"},
		ClockInKeywords:            []string{"上班", "打卡上班", "check in"},
		ClockOutKeywords:           []string{"下班", "打卡下班", "check out"},
		BreakStartKeywords:         []string{"休息", "离开工作", "go to break", "break", "start break"},
		BreakEndKeywords:           []string{"回来工作", "回到工作", "结束休息", "come from break", "back to work", "end break"},
		InteractiveKeywords:        []string{"1", "打卡", "attendance", "check"},
		InteractiveTriggerKeywords: []string{"/start", "start"},
		InteractionExitKeywords:    []string{"exit", "quit", "close", "clos", "closure"},
		CancelKeywords:             []string{"取消", "取消打卡", "取消上班", "取消下班", "取消休息"},
		DefaultAttendanceMode:      "interactive",
		ChatAttendanceModes:        map[string]string{},
		ListKeyword:                "list",
		SummaryKeyword:             "slist",
		PollTimeoutSeconds:         25,
		SummaryKeepMonths:          3,
		MonthlySummaryEnabled:      true,
		MonthlySummaryDay:          1,
		MonthlySummaryHour:         15,
		MonthlySummaryMinute:       0,
		MonthlySummaryChatIDs:      []int64{},
		MotivationEnabled:          true,
		MotivationRemoteEnabled:    true,
		MotivationCandidateCount:   8,
		MotivationTimeoutSeconds:   2,
		MotivationLogFailures:      false,
		ManualCommandDedupeSeconds: 20,
		ReportMultiFormatEnabled:   false,
		UserAliases:                map[string]string{},
	}
}

func loadConfig() (Config, error) {
	cfg := defaultConfig()

	configPath := strings.TrimSpace(os.Getenv("CONFIG_FILE"))
	if configPath == "" {
		configPath = "config.json"
	}

	if b, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("读取配置文件失败 %s: %w", configPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}

	if v := strings.TrimSpace(os.Getenv("BOT_TOKEN")); v != "" {
		cfg.BotToken = v
	}
	if v := strings.TrimSpace(os.Getenv("ALLOWED_CHAT_IDS")); v != "" {
		ids, err := parseChatIDs(v)
		if err != nil {
			return cfg, err
		}
		cfg.AllowedChatIDs = ids
	}
	if v := strings.TrimSpace(os.Getenv("TIMEZONE")); v != "" {
		cfg.Timezone = v
	}
	if v := strings.TrimSpace(os.Getenv("DATA_DIR")); v != "" {
		cfg.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("DATA_FILE")); v != "" {
		cfg.DataFile = v
	}
	if v := strings.TrimSpace(os.Getenv("REPORTS_DIR")); v != "" {
		cfg.ReportsDir = v
	}
	if v := strings.TrimSpace(os.Getenv("STATE_FILE")); v != "" {
		cfg.StateFile = v
	}
	if v := strings.TrimSpace(os.Getenv("LOCK_FILE")); v != "" {
		cfg.LockFile = v
	}
	if v := strings.TrimSpace(os.Getenv("SUMMARY_KEEP_MONTHS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("SUMMARY_KEEP_MONTHS 必须是大于 0 的整数: %s", v)
		}
		cfg.SummaryKeepMonths = n
	}
	if v := strings.TrimSpace(os.Getenv("MONTHLY_SUMMARY_ENABLED")); v != "" {
		cfg.MonthlySummaryEnabled = parseBoolValue(v)
	}
	if v := strings.TrimSpace(os.Getenv("MONTHLY_SUMMARY_DAY")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 28 {
			return cfg, fmt.Errorf("MONTHLY_SUMMARY_DAY 必须是 1-28 的整数: %s", v)
		}
		cfg.MonthlySummaryDay = n
	}
	if v := strings.TrimSpace(os.Getenv("MONTHLY_SUMMARY_HOUR")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 23 {
			return cfg, fmt.Errorf("MONTHLY_SUMMARY_HOUR 必须是 0-23 的整数: %s", v)
		}
		cfg.MonthlySummaryHour = n
	}
	if v := strings.TrimSpace(os.Getenv("MONTHLY_SUMMARY_MINUTE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 59 {
			return cfg, fmt.Errorf("MONTHLY_SUMMARY_MINUTE 必须是 0-59 的整数: %s", v)
		}
		cfg.MonthlySummaryMinute = n
	}
	if v := strings.TrimSpace(os.Getenv("MONTHLY_SUMMARY_CHAT_IDS")); v != "" {
		ids, err := parseChatIDs(v)
		if err != nil {
			return cfg, err
		}
		cfg.MonthlySummaryChatIDs = ids
	}
	if v := strings.TrimSpace(os.Getenv("DEFAULT_ATTENDANCE_MODE")); v != "" {
		cfg.DefaultAttendanceMode = v
	}
	if v := strings.TrimSpace(os.Getenv("CHAT_ATTENDANCE_MODES")); v != "" {
		modes, err := parseChatAttendanceModes(v)
		if err != nil {
			return cfg, err
		}
		cfg.ChatAttendanceModes = modes
	}

	if v := strings.TrimSpace(os.Getenv("INTERACTIVE_TRIGGER_KEYWORDS")); v != "" {
		cfg.InteractiveTriggerKeywords = parseStringList(v)
	}
	if v := strings.TrimSpace(os.Getenv("INTERACTION_EXIT_KEYWORDS")); v != "" {
		cfg.InteractionExitKeywords = parseStringList(v)
	}
	if v := strings.TrimSpace(os.Getenv("MOTIVATION_LOG_FAILURES")); v != "" {
		cfg.MotivationLogFailures = parseBoolValue(v)
	}
	if v := strings.TrimSpace(os.Getenv("MANUAL_COMMAND_DEDUPE_SECONDS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("MANUAL_COMMAND_DEDUPE_SECONDS 必须是大于等于 0 的整数: %s", v)
		}
		cfg.ManualCommandDedupeSeconds = n
	}
	if v := strings.TrimSpace(os.Getenv("REPORT_MULTI_FORMAT_ENABLED")); v != "" {
		cfg.ReportMultiFormatEnabled = parseBoolValue(v)
	}
	if v := strings.TrimSpace(os.Getenv("DEBUG")); v != "" {
		cfg.Debug = strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
	}

	if cfg.BotToken == "" || strings.Contains(cfg.BotToken, "PUT_YOUR") {
		return cfg, errors.New("缺少 bot_token：请在 config.json 或环境变量 BOT_TOKEN 中配置 Telegram Bot Token")
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Dubai"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data/chats"
	}
	if cfg.DataFile == "" {
		cfg.DataFile = "data/attendance.jsonl"
	}
	if cfg.ReportsDir == "" {
		cfg.ReportsDir = "reports"
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "data/bot_state.json"
	}
	if cfg.LockFile == "" {
		cfg.LockFile = "data/bot.lock"
	}
	if cfg.ListKeyword == "" {
		cfg.ListKeyword = "list"
	}
	if cfg.SummaryKeyword == "" {
		cfg.SummaryKeyword = "slist"
	}
	if cfg.PollTimeoutSeconds <= 0 {
		cfg.PollTimeoutSeconds = 25
	}
	if cfg.SummaryKeepMonths <= 0 {
		cfg.SummaryKeepMonths = 3
	}
	if cfg.MonthlySummaryDay <= 0 {
		cfg.MonthlySummaryDay = 1
	}
	if cfg.MonthlySummaryDay > 28 {
		return cfg, fmt.Errorf("monthly_summary_day 必须是 1-28 的整数，当前为 %d", cfg.MonthlySummaryDay)
	}
	if cfg.MonthlySummaryHour < 0 || cfg.MonthlySummaryHour > 23 {
		return cfg, fmt.Errorf("monthly_summary_hour 必须是 0-23 的整数，当前为 %d", cfg.MonthlySummaryHour)
	}
	if cfg.MonthlySummaryMinute < 0 || cfg.MonthlySummaryMinute > 59 {
		return cfg, fmt.Errorf("monthly_summary_minute 必须是 0-59 的整数，当前为 %d", cfg.MonthlySummaryMinute)
	}
	if cfg.MonthlySummaryChatIDs == nil {
		cfg.MonthlySummaryChatIDs = []int64{}
	}
	if cfg.MotivationCandidateCount <= 0 {
		cfg.MotivationCandidateCount = 8
	}
	if cfg.MotivationCandidateCount > 20 {
		cfg.MotivationCandidateCount = 20
	}
	if cfg.MotivationTimeoutSeconds <= 0 {
		cfg.MotivationTimeoutSeconds = 2
	}
	if cfg.ManualCommandDedupeSeconds < 0 {
		cfg.ManualCommandDedupeSeconds = 20
	}
	if cfg.UserAliases == nil {
		cfg.UserAliases = map[string]string{}
	}
	if len(cfg.TriggerKeywords) == 0 {
		cfg.TriggerKeywords = []string{"1"}
	}
	if len(cfg.ClockInKeywords) == 0 {
		cfg.ClockInKeywords = []string{"上班", "打卡上班", "check in"}
	}
	if len(cfg.ClockOutKeywords) == 0 {
		cfg.ClockOutKeywords = []string{"下班", "打卡下班", "check out"}
	}
	if len(cfg.BreakStartKeywords) == 0 {
		cfg.BreakStartKeywords = []string{"休息", "离开工作", "go to break", "break", "start break"}
	}
	if len(cfg.BreakEndKeywords) == 0 {
		cfg.BreakEndKeywords = []string{"回来工作", "回到工作", "结束休息", "come from break", "back to work", "end break"}
	}
	if len(cfg.InteractiveKeywords) == 0 {
		cfg.InteractiveKeywords = []string{"1", "打卡", "attendance", "check"}
	}
	if len(cfg.InteractiveTriggerKeywords) == 0 {
		cfg.InteractiveTriggerKeywords = []string{"/start", "start"}
	}
	if len(cfg.InteractionExitKeywords) == 0 {
		cfg.InteractionExitKeywords = []string{"exit", "quit", "close", "clos", "closure"}
	}
	if len(cfg.CancelKeywords) == 0 {
		cfg.CancelKeywords = []string{"取消", "取消打卡", "取消上班", "取消下班", "取消休息"}
	}
	cfg.DefaultAttendanceMode = normalizeAttendanceMode(cfg.DefaultAttendanceMode)
	if cfg.DefaultAttendanceMode == "" {
		cfg.DefaultAttendanceMode = "interactive"
	}
	if cfg.ChatAttendanceModes == nil {
		cfg.ChatAttendanceModes = map[string]string{}
	}
	for chatID, mode := range cfg.ChatAttendanceModes {
		normalized := normalizeAttendanceMode(mode)
		if normalized == "" {
			return cfg, fmt.Errorf("chat_attendance_modes[%s] 模式无效: %s，可用值: interactive/simple/all", chatID, mode)
		}
		cfg.ChatAttendanceModes[chatID] = normalized
	}
	return cfg, nil
}

func parseChatIDs(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("chat_id 不是整数: %s", p)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseStringList(s string) []string {
	items := strings.Split(s, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func parseChatAttendanceModes(s string) (map[string]string, error) {
	modes := map[string]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.FieldsFunc(item, func(r rune) bool { return r == ':' || r == '=' })
		if len(parts) != 2 {
			return nil, fmt.Errorf("CHAT_ATTENDANCE_MODES 格式错误: %s，示例 -1001:interactive,-1002:simple", item)
		}
		chatID := strings.TrimSpace(parts[0])
		mode := normalizeAttendanceMode(parts[1])
		if chatID == "" || mode == "" {
			return nil, fmt.Errorf("CHAT_ATTENDANCE_MODES 包含无效项: %s", item)
		}
		modes[chatID] = mode
	}
	return modes, nil
}

func normalizeAttendanceMode(mode string) string {
	s := strings.ToLower(strings.TrimSpace(mode))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "", "interactive", "popup", "button", "buttons", "inline", "menu":
		if s == "" {
			return ""
		}
		return "interactive"
	case "simple", "keyword", "keywords", "legacy", "text":
		return "simple"
	case "all", "full", "both", "mixed":
		return "all"
	default:
		return ""
	}
}

func parseBoolValue(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes" || v == "y" || v == "on" || v == "enable" || v == "enabled"
}
