package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// =========================
// Bot 业务逻辑
// =========================

type Session struct {
	Token            string
	Mode             string
	ChatID           int64
	MessageID        int
	TriggerMessageID int
	UserID           int64
	Name             string
	MentionPlain     string
	MentionHTML      string
	CancelKind       string
	Loc              *time.Location
	Records          []AttendanceRecord
	Selected         map[int]bool
	ExpireAt         time.Time
}

type Bot struct {
	cfg        Config
	loc        *time.Location
	tg         *TelegramClient
	sessions   map[string]Session
	sessMutex  sync.Mutex
	stateMutex sync.Mutex
	lockFile   *os.File
}

func NewBot(cfg Config, loc *time.Location) *Bot {
	return &Bot{
		cfg:      cfg,
		loc:      loc,
		tg:       NewTelegramClient(cfg.BotToken, cfg.Debug),
		sessions: map[string]Session{},
	}
}

func (b *Bot) storeForChat(chatID int64) *Store {
	return NewStore(b.dataFileForChat(chatID))
}

func (b *Bot) dataFileForChat(chatID int64) string {
	root := strings.TrimSpace(b.cfg.DataDir)
	if root == "" {
		root = "data/chats"
	}
	return filepath.Join(root, chatIDPathPart(chatID), "attendance.jsonl")
}

func (b *Bot) reportsDirForChat(chatID int64) string {
	root := strings.TrimSpace(b.cfg.ReportsDir)
	if root == "" {
		root = "reports"
	}
	return filepath.Join(root, "chats", chatIDPathPart(chatID))
}

func chatIDPathPart(chatID int64) string {
	return "chat_" + strconv.FormatInt(chatID, 10)
}

func (b *Bot) appendRecordForChat(rec AttendanceRecord) error {
	return b.storeForChat(rec.ChatID).Append(rec)
}

func (b *Bot) readRecordsForChat(chatID int64) ([]AttendanceRecord, error) {
	merged := []AttendanceRecord{}
	seen := map[string]bool{}
	add := func(items []AttendanceRecord) {
		for _, rec := range items {
			if rec.ChatID != chatID {
				continue
			}
			key := recordKey(rec)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, rec)
		}
	}
	chatRecords, err := b.storeForChat(chatID).ReadAll(b.loc)
	if err != nil {
		return nil, err
	}
	add(chatRecords)
	legacyPath := strings.TrimSpace(b.cfg.DataFile)
	if legacyPath != "" && filepath.Clean(legacyPath) != filepath.Clean(b.dataFileForChat(chatID)) {
		legacyRecords, err := NewStore(legacyPath).ReadAll(b.loc)
		if err != nil {
			return nil, err
		}
		add(legacyRecords)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].At(b.loc).Before(merged[j].At(b.loc)) })
	return merged, nil
}

func (b *Bot) deleteRecordsForChat(chatID int64, targets []AttendanceRecord) (int, error) {
	deleted, err := b.storeForChat(chatID).DeleteMatching(targets, b.loc)
	if err != nil {
		return deleted, err
	}
	legacyPath := strings.TrimSpace(b.cfg.DataFile)
	if legacyPath != "" && filepath.Clean(legacyPath) != filepath.Clean(b.dataFileForChat(chatID)) {
		n, err := NewStore(legacyPath).DeleteMatching(targets, b.loc)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (b *Bot) knownChatIDsFromData() []int64 {
	ids := []int64{}
	root := strings.TrimSpace(b.cfg.DataDir)
	if root == "" {
		root = "data/chats"
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := strings.TrimPrefix(entry.Name(), "chat_")
			if id, err := strconv.ParseInt(name, 10, 64); err == nil && id != 0 {
				ids = append(ids, id)
			}
		}
	}
	legacyPath := strings.TrimSpace(b.cfg.DataFile)
	if legacyPath != "" {
		legacyRecords, err := NewStore(legacyPath).ReadAll(b.loc)
		if err == nil {
			for _, rec := range legacyRecords {
				if rec.ChatID != 0 {
					ids = append(ids, rec.ChatID)
				}
			}
		}
	}
	return uniqueSortedChatIDs(ids)
}

func (b *Bot) attendanceModeForChat(chatID int64) string {
	if len(b.cfg.ChatAttendanceModes) > 0 {
		keys := []string{strconv.FormatInt(chatID, 10), chatIDPathPart(chatID)}
		for _, key := range keys {
			if mode := normalizeAttendanceMode(b.cfg.ChatAttendanceModes[key]); mode != "" {
				return mode
			}
		}
	}
	mode := normalizeAttendanceMode(b.cfg.DefaultAttendanceMode)
	if mode == "" {
		return "interactive"
	}
	return mode
}

func ensureParentDir(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

func (b *Bot) acquireLocalLock() error {
	if strings.TrimSpace(b.cfg.LockFile) == "" {
		return nil
	}
	f, err := os.OpenFile(b.cfg.LockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("创建本机锁文件失败 %s: %w", b.cfg.LockFile, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return fmt.Errorf("启动失败：检测到同一台机器上已有机器人实例在运行，锁文件 %s 正被占用；请先停止旧进程/容器/服务，确认没有进程后再重启", b.cfg.LockFile)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteString(fmt.Sprintf("pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().Format(time.RFC3339)))
	b.lockFile = f
	return nil
}

func (b *Bot) releaseLocalLock() {
	if b.lockFile == nil {
		return
	}
	_ = syscall.Flock(int(b.lockFile.Fd()), syscall.LOCK_UN)
	_ = b.lockFile.Close()
	_ = os.Remove(b.cfg.LockFile)
	b.lockFile = nil
}

type BotState struct {
	Offset             int64             `json:"offset"`
	UpdatedAt          string            `json:"updated_at"`
	MonthlySummarySent map[string]string `json:"monthly_summary_sent,omitempty"`
}

func (b *Bot) readStateNoLock() BotState {
	st := BotState{MonthlySummarySent: map[string]string{}}
	if strings.TrimSpace(b.cfg.StateFile) == "" {
		return st
	}
	data, err := os.ReadFile(b.cfg.StateFile)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("读取状态文件失败，将使用空状态继续: %v", err)
		return BotState{MonthlySummarySent: map[string]string{}}
	}
	if st.MonthlySummarySent == nil {
		st.MonthlySummarySent = map[string]string{}
	}
	return st
}

func (b *Bot) writeStateNoLock(st BotState) {
	if strings.TrimSpace(b.cfg.StateFile) == "" {
		return
	}
	if st.MonthlySummarySent == nil {
		st.MonthlySummarySent = map[string]string{}
	}
	st.UpdatedAt = time.Now().In(b.loc).Format(time.RFC3339)
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(b.cfg.StateFile, data, 0644); err != nil {
		log.Printf("保存状态文件失败: %v", err)
	}
}

func (b *Bot) loadOffset() int64 {
	b.stateMutex.Lock()
	defer b.stateMutex.Unlock()
	st := b.readStateNoLock()
	return st.Offset
}

func (b *Bot) saveOffset(offset int64) {
	b.stateMutex.Lock()
	defer b.stateMutex.Unlock()
	st := b.readStateNoLock()
	st.Offset = offset
	b.writeStateNoLock(st)
}

func (b *Bot) monthlySummaryWasSent(chatID int64, monthKey string) bool {
	b.stateMutex.Lock()
	defer b.stateMutex.Unlock()
	st := b.readStateNoLock()
	_, ok := st.MonthlySummarySent[monthlySummaryStateKey(chatID, monthKey)]
	return ok
}

func (b *Bot) markMonthlySummarySent(chatID int64, monthKey string, sentAt time.Time) {
	b.stateMutex.Lock()
	defer b.stateMutex.Unlock()
	st := b.readStateNoLock()
	if st.MonthlySummarySent == nil {
		st.MonthlySummarySent = map[string]string{}
	}
	st.MonthlySummarySent[monthlySummaryStateKey(chatID, monthKey)] = sentAt.In(b.loc).Format(time.RFC3339)
	cutoff := startOfMonth(sentAt.In(b.loc)).AddDate(0, -(b.cfg.SummaryKeepMonths + 2), 0)
	for key := range st.MonthlySummarySent {
		idx := strings.LastIndex(key, ":")
		if idx < 0 || idx+1 >= len(key) {
			continue
		}
		monthText := key[idx+1:]
		monthTime, err := time.ParseInLocation("2006-01", monthText, b.loc)
		if err == nil && monthTime.Before(cutoff) {
			delete(st.MonthlySummarySent, key)
		}
	}
	b.writeStateNoLock(st)
}

func monthlySummaryStateKey(chatID int64, monthKey string) string {
	return fmt.Sprintf("%d:%s", chatID, monthKey)
}

func isTelegramConflictError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "HTTP 409") || strings.Contains(s, "Conflict") || strings.Contains(s, "terminated by other getUpdates request")
}

func (b *Bot) Run() error {
	if strings.TrimSpace(b.cfg.DataDir) != "" {
		if err := os.MkdirAll(b.cfg.DataDir, 0755); err != nil {
			return err
		}
	}
	if err := ensureParentDir(b.cfg.DataFile); err != nil {
		return err
	}
	if err := ensureParentDir(b.cfg.StateFile); err != nil {
		return err
	}
	if err := ensureParentDir(b.cfg.LockFile); err != nil {
		return err
	}
	if err := os.MkdirAll(b.cfg.ReportsDir, 0755); err != nil {
		return err
	}
	if err := b.acquireLocalLock(); err != nil {
		return err
	}
	defer b.releaseLocalLock()

	if err := b.tg.DeleteWebhook(); err != nil {
		log.Printf("deleteWebhook 失败，继续尝试长轮询: %v", err)
	}

	log.Printf("打卡机器人已启动")
	log.Printf("版本: %s", version)
	log.Printf("群数据目录: %s，兼容旧数据文件: %s，报表根目录: %s，时区: %s", b.cfg.DataDir, b.cfg.DataFile, b.cfg.ReportsDir, b.cfg.Timezone)
	log.Printf("状态文件: %s，本机锁: %s，slist 保留月份: %d", b.cfg.StateFile, b.cfg.LockFile, b.cfg.SummaryKeepMonths)
	log.Printf("月度汇总：enabled=%v day=%d time=%02d:%02d target_chats=%v", b.cfg.MonthlySummaryEnabled, b.cfg.MonthlySummaryDay, b.cfg.MonthlySummaryHour, b.cfg.MonthlySummaryMinute, b.cfg.MonthlySummaryChatIDs)
	log.Printf("打卡模式：default=%s chat_modes=%v", b.cfg.DefaultAttendanceMode, b.cfg.ChatAttendanceModes)
	log.Printf("打卡识别：上班=%s；下班=%s；休息=%s；返回=%s；交互启动=%s；交互退出=%s；取消=%s；报表=%s/%s", strings.Join(b.cfg.ClockInKeywords, "/"), strings.Join(b.cfg.ClockOutKeywords, "/"), strings.Join(b.cfg.BreakStartKeywords, "/"), strings.Join(b.cfg.BreakEndKeywords, "/"), strings.Join(b.cfg.InteractiveTriggerKeywords, "/"), strings.Join(b.cfg.InteractionExitKeywords, "/"), strings.Join(b.cfg.CancelKeywords, "/"), b.cfg.ListKeyword, b.cfg.SummaryKeyword)
	log.Printf("激励语：enabled=%v remote=%v languages=zh/ja/en batch=%d timeout=%ds", b.cfg.MotivationEnabled, b.cfg.MotivationRemoteEnabled, b.cfg.MotivationCandidateCount, b.cfg.MotivationTimeoutSeconds)
	if len(b.cfg.AllowedChatIDs) == 0 {
		log.Printf("ALLOWED_CHAT_IDS 为空：当前允许所有群。建议测试成功后配置指定群 chat_id。")
	} else {
		log.Printf("限定群 chat_id: %v", b.cfg.AllowedChatIDs)
	}

	offset := b.loadOffset()
	if offset > 0 {
		log.Printf("从状态文件恢复 update offset: %d", offset)
	}
	if b.cfg.MonthlySummaryEnabled {
		go b.runMonthlySummaryScheduler()
	} else {
		log.Printf("月度汇总定时任务已关闭")
	}

	for {
		updates, err := b.tg.GetUpdates(offset, b.cfg.PollTimeoutSeconds)
		if err != nil {
			if isTelegramConflictError(err) {
				log.Printf("getUpdates 冲突: 当前 Bot Token 正在被另一个 getUpdates 实例占用。请停止其它进程/容器/服务，或改用不同 token。原始错误: %v", err)
				time.Sleep(15 * time.Second)
				continue
			}
			log.Printf("getUpdates 失败: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, up := range updates {
			if up.UpdateID >= offset {
				offset = up.UpdateID + 1
			}
			b.handleUpdate(up)
		}
		if len(updates) > 0 {
			b.saveOffset(offset)
		}
	}
}

func (b *Bot) runMonthlySummaryScheduler() {
	log.Printf("月度汇总定时任务已启动：每月 %d 号 %02d:%02d 后检查并发送上月全员 XLSX 工时统计表", b.cfg.MonthlySummaryDay, b.cfg.MonthlySummaryHour, b.cfg.MonthlySummaryMinute)
	b.trySendMonthlySummary(time.Now().In(b.loc))
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for t := range ticker.C {
		b.trySendMonthlySummary(t.In(b.loc))
	}
}

func (b *Bot) monthlySummaryDue(now time.Time) (time.Time, string, bool) {
	now = now.In(b.loc)
	if !b.cfg.MonthlySummaryEnabled {
		return time.Time{}, "", false
	}
	day := b.cfg.MonthlySummaryDay
	if day <= 0 {
		day = 1
	}
	hour := b.cfg.MonthlySummaryHour
	minute := b.cfg.MonthlySummaryMinute
	if now.Day() != day {
		return time.Time{}, "", false
	}
	scheduledAt := time.Date(now.Year(), now.Month(), day, hour, minute, 0, 0, b.loc)
	if now.Before(scheduledAt) {
		return time.Time{}, "", false
	}
	reportMonth := startOfMonth(now).AddDate(0, -1, 0)
	return reportMonth, reportMonth.Format("2006-01"), true
}

func (b *Bot) trySendMonthlySummary(now time.Time) {
	reportMonth, monthKey, ok := b.monthlySummaryDue(now)
	if !ok {
		return
	}
	targets := b.monthlySummaryTargetChatIDs()
	if len(targets) == 0 {
		log.Printf("自动月度汇总未发送：未配置 monthly_summary_chat_ids/allowed_chat_ids，且无法从群数据目录或旧数据文件推断群 chat_id")
		return
	}
	for _, chatID := range targets {
		if b.monthlySummaryWasSent(chatID, monthKey) {
			continue
		}
		records, err := b.readRecordsForChat(chatID)
		if err != nil {
			log.Printf("自动月度汇总读取打卡数据失败 chat_id=%d: %v", chatID, err)
			continue
		}
		if err := b.generateAndSendSummaryReport(chatID, records, reportMonth, now, true); err != nil {
			log.Printf("自动月度汇总发送失败 chat_id=%d month=%s: %v", chatID, monthKey, err)
			continue
		}
		b.markMonthlySummarySent(chatID, monthKey, now)
		log.Printf("自动月度汇总已发送 chat_id=%d month=%s", chatID, monthKey)
	}
}

func (b *Bot) monthlySummaryTargetChatIDs() []int64 {
	ids := []int64{}
	if len(b.cfg.MonthlySummaryChatIDs) > 0 {
		ids = append(ids, b.cfg.MonthlySummaryChatIDs...)
	} else if len(b.cfg.AllowedChatIDs) > 0 {
		ids = append(ids, b.cfg.AllowedChatIDs...)
	} else {
		ids = append(ids, b.knownChatIDsFromData()...)
	}
	return uniqueSortedChatIDs(ids)
}

func uniqueSortedChatIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (b *Bot) generateAndSendSummaryReport(chatID int64, records []AttendanceRecord, reportMonth time.Time, generatedAt time.Time, automated bool) error {
	reportMonth = startOfMonth(reportMonth.In(b.loc))
	generatedAt = generatedAt.In(b.loc)
	var reports map[int64]*UserMonthReport
	if automated || !startOfMonth(generatedAt).Equal(reportMonth) {
		reports = BuildReportsForFullMonth(records, chatID, b.loc, reportMonth, generatedAt)
	} else {
		reports = BuildReports(records, chatID, b.loc, generatedAt)
	}
	b.applyConfiguredAliasesToReports(reports)
	reportsDir := b.reportsDirForChat(chatID)
	path, err := GenerateSummaryXLSXForMonth(reports, b.loc, reportsDir, reportMonth)
	if err != nil {
		return fmt.Errorf("生成 XLSX 汇总失败: %w", err)
	}
	cleanupOldSummaryReports(reportsDir, b.cfg.SummaryKeepMonths, b.loc, generatedAt)
	caption := b.summaryCaption(reportMonth, generatedAt, automated)
	if err := b.tg.SendDocument(chatID, path, caption); err != nil {
		return fmt.Errorf("发送 XLSX 汇总失败: %w", err)
	}
	return nil
}

func (b *Bot) summaryCaption(reportMonth time.Time, generatedAt time.Time, automated bool) string {
	reportMonth = startOfMonth(reportMonth.In(b.loc))
	monthEnd := reportMonth.AddDate(0, 1, -1)
	if automated {
		return fmt.Sprintf("📊 自动月度汇总：全员 %s 打卡工时统计表\n统计范围：%s ~ %s\n生成时间：%s", reportMonth.Format("2006-01"), reportMonth.Format("2006-01-02"), monthEnd.Format("2006-01-02"), generatedAt.In(b.loc).Format("2006-01-02 15:04:05"))
	}
	return fmt.Sprintf("已更新最新数据：全员 %s 打卡汇总，生成时间 %s", reportMonth.Format("2006-01"), generatedAt.In(b.loc).Format("2006-01-02 15:04:05"))
}

func (b *Bot) handleUpdate(up Update) {
	if up.Message != nil {
		b.handleMessage(up.Message)
		return
	}
	if up.CallbackQuery != nil {
		b.handleCallback(up.CallbackQuery)
		return
	}
}

func (b *Bot) handleMessage(msg *Message) {
	if msg.From == nil {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	if b.cfg.Debug {
		log.Printf("收到消息 chat_id=%d chat_type=%s from=%s user_id=%d text=%q", msg.Chat.ID, msg.Chat.Type, b.displayLabelFromUser(*msg.From), msg.From.ID, text)
	}
	if !b.chatAllowed(msg.Chat.ID) {
		if b.cfg.Debug {
			log.Printf("忽略消息：chat_id=%d 不在 allowed_chat_ids 中", msg.Chat.ID)
		}
		return
	}

	cmd := normalizeCommand(text)
	summaryCmd := normalizeCommand(b.cfg.SummaryKeyword)
	listCmd := normalizeCommand(b.cfg.ListKeyword)

	// 注意：必须先判断 slist，再判断 list，避免 slist 被 list 抢走。
	if cmd == summaryCmd || cmd == "/"+summaryCmd {
		if b.cfg.Debug {
			log.Printf("命中 slist 指令：生成全员 XLSX 汇总")
		}
		b.handleSummaryReport(msg)
		return
	}
	if cmd == listCmd || cmd == "/"+listCmd {
		if b.cfg.Debug {
			log.Printf("命中 list 指令：生成个人日历 HTML")
		}
		b.handlePersonalReport(msg)
		return
	}

	if b.detectInteractionExit(text) {
		if b.closeUserSessions(msg.Chat.ID, msg.From.ID, msg.MessageID) && b.cfg.Debug {
			log.Printf("用户主动关闭交互会话 chat_id=%d user_id=%d", msg.Chat.ID, msg.From.ID)
		}
		return
	}

	if cancelKind, ok := b.detectCancelIntent(text); ok {
		if b.cfg.Debug {
			log.Printf("命中取消打卡指令：%q cancel_kind=%s", text, cancelKind)
		}
		b.promptCancelAttendance(msg, cancelKind)
		return
	}

	mode := b.attendanceModeForChat(msg.Chat.ID)
	directIntent := b.detectDirectAttendanceIntent(text)
	interactiveTrigger := b.detectInteractiveTrigger(text)

	if directIntent != "" || interactiveTrigger {
		if b.cfg.Debug {
			log.Printf("命中打卡入口：text=%q direct_intent=%s interactive_trigger=%v mode=%s", text, directIntent, interactiveTrigger, mode)
		}
		switch mode {
		case "simple":
			if directIntent != "" {
				b.recordAttendanceFromMessage(msg, directIntent)
			}
			return
		case "all":
			if directIntent != "" {
				b.recordAttendanceFromMessage(msg, directIntent)
				return
			}
			if interactiveTrigger {
				b.promptAttendance(msg)
			}
			return
		default:
			if interactiveTrigger {
				b.promptAttendance(msg)
			}
			return
		}
	}

	if b.cfg.Debug {
		log.Printf("消息未命中任何指令: %q", text)
	}
}

func (b *Bot) handlePersonalReport(msg *Message) {
	records, err := b.readRecordsForChat(msg.Chat.ID)
	if err != nil {
		_, _ = b.tg.SendMessage(msg.Chat.ID, "❌ 读取打卡数据失败: "+err.Error(), nil)
		return
	}
	reports := BuildReports(records, msg.Chat.ID, b.loc, time.Now().In(b.loc))
	b.applyConfiguredAliasesToReports(reports)
	rep := reports[msg.From.ID]
	if rep == nil {
		rep = emptyUserReport(msg.Chat.ID, *msg.From, b.loc, time.Now().In(b.loc))
	}
	rep.DisplayName = b.displayLabelFromUser(*msg.From)
	rep.Username = msg.From.Username
	path, err := GenerateHTMLReport(rep, b.loc, b.reportsDirForChat(msg.Chat.ID))
	if err != nil {
		_, _ = b.tg.SendMessage(msg.Chat.ID, "❌ 生成 HTML 报表失败: "+err.Error(), nil)
		return
	}
	caption := fmt.Sprintf("%s 的 %s 打卡日历报表", rep.DisplayName, rep.MonthStart.Format("2006-01"))
	if err := b.tg.SendDocument(msg.Chat.ID, path, caption); err != nil {
		_, _ = b.tg.SendMessage(msg.Chat.ID, "❌ 发送 HTML 报表失败: "+err.Error(), nil)
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("清理个人 HTML 报表失败 %s: %v", path, err)
	}
}

func (b *Bot) handleSummaryReport(msg *Message) {
	records, err := b.readRecordsForChat(msg.Chat.ID)
	if err != nil {
		_, _ = b.tg.SendMessage(msg.Chat.ID, "❌ 读取打卡数据失败: "+err.Error(), nil)
		return
	}
	now := time.Now().In(b.loc)
	if err := b.generateAndSendSummaryReport(msg.Chat.ID, records, now, now, false); err != nil {
		_, _ = b.tg.SendMessage(msg.Chat.ID, "❌ "+err.Error(), nil)
		return
	}
}

func cleanupOldSummaryReports(reportsDir string, keepMonths int, loc *time.Location, now time.Time) {
	if keepMonths <= 0 {
		keepMonths = 3
	}
	cutoff := startOfMonth(now.In(loc)).AddDate(0, -(keepMonths - 1), 0)
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		log.Printf("读取报表目录失败，跳过 slist 清理: %v", err)
		return
	}
	const prefix = "全员_"
	const suffix = "_打卡汇总.xlsx"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		monthText := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		monthTime, err := time.ParseInLocation("2006-01", monthText, loc)
		if err != nil {
			continue
		}
		if monthTime.Before(cutoff) {
			path := filepath.Join(reportsDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("清理旧 slist 汇总失败 %s: %v", path, err)
			} else {
				log.Printf("已清理旧 slist 汇总: %s", path)
			}
		}
	}
}

func emptyUserReport(chatID int64, user User, loc *time.Location, now time.Time) *UserMonthReport {
	monthStart := startOfMonth(now.In(loc))
	monthEnd := monthStart.AddDate(0, 1, -1)
	displayEnd := startOfDay(now.In(loc))
	if displayEnd.After(monthEnd) {
		displayEnd = monthEnd
	}
	finalizedEnd := startOfDay(now.In(loc)).AddDate(0, 0, -1)
	rep := &UserMonthReport{
		ChatID:       chatID,
		UserID:       user.ID,
		DisplayName:  displayNameFromUser(user),
		Username:     user.Username,
		MonthStart:   monthStart,
		DisplayEnd:   displayEnd,
		FinalizedEnd: finalizedEnd,
		GeneratedAt:  now.In(loc),
		Days:         map[string]*DayReport{},
	}
	for d := monthStart; !d.After(monthEnd); d = d.AddDate(0, 0, 1) {
		rep.Days[dateKey(d)] = &DayReport{Date: d}
	}
	return rep
}

func (b *Bot) recordAttendanceFromMessage(msg *Message, kind string) {
	if msg.From == nil {
		return
	}
	if kindDisplayName(kind) == "" {
		_, _ = b.tg.SendMessageHTML(msg.Chat.ID, b.mentionHTMLFromUser(*msg.From)+" ❌ 无效打卡类型", nil, 0)
		return
	}
	now := time.Now().In(b.loc)
	rec := AttendanceRecord{
		ChatID:      msg.Chat.ID,
		UserID:      msg.From.ID,
		Username:    msg.From.Username,
		FirstName:   msg.From.FirstName,
		LastName:    msg.From.LastName,
		DisplayName: b.displayLabelFromUser(*msg.From),
		Kind:        kind,
		Time:        now.Format(time.RFC3339),
		Unix:        now.Unix(),
	}
	mention := b.mentionHTMLFromUser(*msg.From)
	if err := b.appendRecordForChat(rec); err != nil {
		_, _ = b.tg.SendMessageHTML(msg.Chat.ID, mention+" ❌ 打卡记录失败: "+html.EscapeString(err.Error()), nil, 0)
		return
	}
	_, _ = b.tg.SendMessageHTML(msg.Chat.ID, b.formatAttendanceFeedback(mention, kind, now), nil, 0)
}

func (b *Bot) promptAttendance(msg *Message) {
	b.closeUserSessions(msg.Chat.ID, msg.From.ID, 0)
	token := randomToken()
	name := b.displayLabelFromUser(*msg.From)
	mentionHTML := b.mentionHTMLFromUser(*msg.From)
	mentionPlain := b.mentionPlainFromUser(*msg.From)
	replyMarkup := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": "💻 Check in", "callback_data": "att:" + token + ":in"},
				{"text": "🛌 Check out", "callback_data": "att:" + token + ":out"},
			},
			{
				{"text": "☕ Go to break", "callback_data": "att:" + token + ":break_start"},
				{"text": "↩️ Come from break", "callback_data": "att:" + token + ":break_end"},
			},
			{
				{"text": "❌ Cancel", "callback_data": "att:" + token + ":cancel"},
			},
		},
	}
	prompt := fmt.Sprintf("%s, please choose an attendance action:", mentionHTML)
	sent, err := b.tg.SendMessageHTML(msg.Chat.ID, prompt, replyMarkup, 0)
	if err != nil {
		log.Printf("发送打卡按钮失败: %v", err)
		return
	}
	b.sessMutex.Lock()
	b.sessions[token] = Session{
		Token:            token,
		Mode:             "attendance",
		ChatID:           msg.Chat.ID,
		MessageID:        sent.MessageID,
		TriggerMessageID: msg.MessageID,
		UserID:           msg.From.ID,
		Name:             name,
		MentionPlain:     mentionPlain,
		MentionHTML:      mentionHTML,
		Loc:              b.loc,
		ExpireAt:         time.Now().Add(10 * time.Minute),
	}
	b.sessMutex.Unlock()
}

func (b *Bot) closeUserSessions(chatID, userID int64, userMessageID int) bool {
	type deleteTarget struct {
		ChatID    int64
		MessageID int
	}
	targets := []deleteTarget{}
	closed := false

	b.sessMutex.Lock()
	for token, sess := range b.sessions {
		if sess.ChatID != chatID || sess.UserID != userID {
			continue
		}
		closed = true
		if sess.MessageID > 0 {
			targets = append(targets, deleteTarget{ChatID: sess.ChatID, MessageID: sess.MessageID})
		}
		if sess.TriggerMessageID > 0 {
			targets = append(targets, deleteTarget{ChatID: sess.ChatID, MessageID: sess.TriggerMessageID})
		}
		delete(b.sessions, token)
	}
	b.sessMutex.Unlock()

	if userMessageID > 0 {
		targets = append(targets, deleteTarget{ChatID: chatID, MessageID: userMessageID})
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if target.MessageID <= 0 {
			continue
		}
		key := fmt.Sprintf("%d:%d", target.ChatID, target.MessageID)
		if seen[key] {
			continue
		}
		seen[key] = true
		b.tryDeleteMessage(target.ChatID, target.MessageID)
	}
	return closed
}

func (b *Bot) closeSession(sess Session, deleteTrigger bool) {
	if sess.MessageID > 0 {
		b.tryDeleteMessage(sess.ChatID, sess.MessageID)
	}
	if deleteTrigger && sess.TriggerMessageID > 0 {
		b.tryDeleteMessage(sess.ChatID, sess.TriggerMessageID)
	}
}

func (b *Bot) tryDeleteMessage(chatID int64, messageID int) {
	if messageID <= 0 {
		return
	}
	if err := b.tg.DeleteMessage(chatID, messageID); err != nil && b.cfg.Debug {
		log.Printf("清理 Telegram 消息失败 chat_id=%d message_id=%d: %v", chatID, messageID, err)
	}
}

func (b *Bot) promptCancelAttendance(msg *Message, cancelKind string) {
	if msg.From == nil {
		return
	}
	records, err := b.readRecordsForChat(msg.Chat.ID)
	mentionHTML := b.mentionHTMLFromUser(*msg.From)
	if err != nil {
		_, _ = b.tg.SendMessageHTML(msg.Chat.ID, mentionHTML+" ❌ 读取打卡记录失败: "+html.EscapeString(err.Error()), nil, 0)
		return
	}
	todayRecords := filterTodayUserRecords(records, msg.Chat.ID, msg.From.ID, cancelKind, b.loc, time.Now().In(b.loc))
	if len(todayRecords) == 0 {
		suffix := ""
		if cancelKind == "break" {
			suffix = "休息"
		} else if isAttendanceKind(cancelKind) {
			suffix = kindDisplayName(cancelKind)
		}
		_, _ = b.tg.SendMessageHTML(msg.Chat.ID, fmt.Sprintf("%s 今天没有可取消的%s打卡记录", mentionHTML, html.EscapeString(suffix)), nil, 0)
		return
	}

	token := randomToken()
	sess := Session{
		Token:            token,
		Mode:             "cancel",
		ChatID:           msg.Chat.ID,
		TriggerMessageID: msg.MessageID,
		UserID:           msg.From.ID,
		Name:             b.displayLabelFromUser(*msg.From),
		MentionPlain:     b.mentionPlainFromUser(*msg.From),
		MentionHTML:      mentionHTML,
		CancelKind:       cancelKind,
		Loc:              b.loc,
		Records:          todayRecords,
		Selected:         map[int]bool{},
		ExpireAt:         time.Now().Add(10 * time.Minute),
	}
	// 默认选中最近一次，用户可以继续切换、多选或全选。
	sess.Selected[len(todayRecords)-1] = true
	replyMarkup := cancelKeyboard(sess)
	sent, err := b.tg.SendMessageHTML(msg.Chat.ID, cancelPromptText(sess), replyMarkup, 0)
	if err != nil {
		log.Printf("发送取消打卡按钮失败: %v", err)
		return
	}
	sess.MessageID = sent.MessageID
	b.sessMutex.Lock()
	b.sessions[token] = sess
	b.sessMutex.Unlock()
}

func (b *Bot) handleCallback(cb *CallbackQuery) {
	if cb.Message == nil {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "按钮消息不存在", false)
		return
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 3 || parts[0] != "att" {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "无效操作", false)
		return
	}
	token, action := parts[1], parts[2]

	b.sessMutex.Lock()
	sess, ok := b.sessions[token]
	if ok && time.Now().After(sess.ExpireAt) {
		delete(b.sessions, token)
		ok = false
	}
	b.sessMutex.Unlock()

	if !ok {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "This interaction has expired. Please start again.", true)
		return
	}
	if !b.chatAllowed(sess.ChatID) {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "This chat is not allowed", true)
		return
	}
	if cb.From.ID != sess.UserID {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "This interaction belongs to "+sess.MentionPlain, true)
		return
	}

	if b.cfg.Debug {
		log.Printf("收到按钮回调 user_id=%d mode=%s action=%s token=%s", cb.From.ID, sess.Mode, action, token)
	}

	if sess.Mode == "cancel" {
		b.handleCancelCallback(cb, sess, action)
		return
	}
	b.handleAttendanceCallback(cb, sess, action)
}

func (b *Bot) handleAttendanceCallback(cb *CallbackQuery, sess Session, action string) {
	if action == "cancel" {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "Closed", false)
		b.closeSession(sess, true)
		b.sessMutex.Lock()
		delete(b.sessions, sess.Token)
		b.sessMutex.Unlock()
		return
	}

	kind := normalizeKind(action)
	if !isAttendanceKind(kind) {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "Invalid action", false)
		return
	}

	now := time.Now().In(b.loc)
	rec := AttendanceRecord{
		ChatID:      sess.ChatID,
		UserID:      cb.From.ID,
		Username:    cb.From.Username,
		FirstName:   cb.From.FirstName,
		LastName:    cb.From.LastName,
		DisplayName: b.displayLabelFromUser(cb.From),
		Kind:        kind,
		Time:        now.Format(time.RFC3339),
		Unix:        now.Unix(),
	}
	if err := b.appendRecordForChat(rec); err != nil {
		_ = b.tg.AnswerCallbackQuery(cb.ID, "Record failed", true)
		b.closeSession(sess, true)
		_, _ = b.tg.SendMessageHTML(sess.ChatID, sess.MentionHTML+" ❌ Record failed: "+html.EscapeString(err.Error()), nil, 0)
		b.sessMutex.Lock()
		delete(b.sessions, sess.Token)
		b.sessMutex.Unlock()
		return
	}
	_ = b.tg.AnswerCallbackQuery(cb.ID, "Recorded", false)
	b.closeSession(sess, true)
	_, _ = b.tg.SendMessageHTML(sess.ChatID, b.formatAttendanceFeedbackEnglish(sess.MentionHTML, kind, now), nil, 0)

	b.sessMutex.Lock()
	delete(b.sessions, sess.Token)
	b.sessMutex.Unlock()
}

func (b *Bot) handleCancelCallback(cb *CallbackQuery, sess Session, action string) {
	if sess.Selected == nil {
		sess.Selected = map[int]bool{}
	}

	switch {
	case action == "cancel":
		_ = b.tg.AnswerCallbackQuery(cb.ID, "Closed", false)
		b.closeSession(sess, true)
		b.sessMutex.Lock()
		delete(b.sessions, sess.Token)
		b.sessMutex.Unlock()
		return

	case action == "all":
		if len(sess.Selected) == len(sess.Records) {
			sess.Selected = map[int]bool{}
		} else {
			sess.Selected = map[int]bool{}
			for i := range sess.Records {
				sess.Selected[i] = true
			}
		}
		b.saveSessionAndRefreshCancelMessage(cb, sess, "已更新选择")
		return

	case action == "last":
		sess.Selected = map[int]bool{}
		if len(sess.Records) > 0 {
			sess.Selected[len(sess.Records)-1] = true
		}
		b.saveSessionAndRefreshCancelMessage(cb, sess, "已选择最近一次")
		return

	case strings.HasPrefix(action, "t"):
		idxText := strings.TrimPrefix(action, "t")
		idx, err := strconv.Atoi(idxText)
		if err != nil || idx < 0 || idx >= len(sess.Records) {
			_ = b.tg.AnswerCallbackQuery(cb.ID, "无效记录", false)
			return
		}
		if sess.Selected[idx] {
			delete(sess.Selected, idx)
		} else {
			sess.Selected[idx] = true
		}
		b.saveSessionAndRefreshCancelMessage(cb, sess, "已更新选择")
		return

	case action == "ok":
		selected := selectedCancelRecords(sess)
		if len(selected) == 0 {
			_ = b.tg.AnswerCallbackQuery(cb.ID, "请先选择要取消的记录", true)
			return
		}
		deleted, err := b.deleteRecordsForChat(sess.ChatID, selected)
		if err != nil {
			_ = b.tg.AnswerCallbackQuery(cb.ID, "取消失败", true)
			b.closeSession(sess, true)
			_, _ = b.tg.SendMessageHTML(sess.ChatID, sess.MentionHTML+" ❌ 取消打卡失败: "+html.EscapeString(err.Error()), nil, 0)
			b.sessMutex.Lock()
			delete(b.sessions, sess.Token)
			b.sessMutex.Unlock()
			return
		}
		_ = b.tg.AnswerCallbackQuery(cb.ID, fmt.Sprintf("已取消 %d 条", deleted), false)
		b.closeSession(sess, true)
		_, _ = b.tg.SendMessageHTML(sess.ChatID, cancelDoneText(sess, selected, deleted), nil, 0)
		b.sessMutex.Lock()
		delete(b.sessions, sess.Token)
		b.sessMutex.Unlock()
		return
	}

	_ = b.tg.AnswerCallbackQuery(cb.ID, "无效操作", false)
}

func (b *Bot) saveSessionAndRefreshCancelMessage(cb *CallbackQuery, sess Session, tip string) {
	b.sessMutex.Lock()
	b.sessions[sess.Token] = sess
	b.sessMutex.Unlock()
	_ = b.tg.AnswerCallbackQuery(cb.ID, tip, false)
	_ = b.tg.EditMessageTextHTMLWithMarkup(sess.ChatID, sess.MessageID, cancelPromptText(sess), cancelKeyboard(sess))
}

func filterTodayUserRecords(records []AttendanceRecord, chatID, userID int64, kind string, loc *time.Location, now time.Time) []AttendanceRecord {
	start := startOfDay(now.In(loc))
	end := start.AddDate(0, 0, 1)
	out := make([]AttendanceRecord, 0)
	for _, rec := range records {
		if rec.ChatID != chatID || rec.UserID != userID {
			continue
		}
		if kind == "break" {
			if rec.Kind != "break_start" && rec.Kind != "break_end" {
				continue
			}
		} else if kind != "" && rec.Kind != kind {
			continue
		}
		t := rec.At(loc)
		if !t.Before(start) && t.Before(end) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At(loc).Before(out[j].At(loc)) })
	return out
}

func cancelPromptText(sess Session) string {
	var b strings.Builder
	b.WriteString(sess.MentionHTML)
	b.WriteString("，请选择要取消的打卡记录：")
	if sess.CancelKind == "break" {
		b.WriteString("\n仅显示休息相关记录。")
	} else if isAttendanceKind(sess.CancelKind) {
		b.WriteString("\n仅显示")
		b.WriteString(kindDisplayName(sess.CancelKind))
		b.WriteString("记录。")
	}
	b.WriteString("\n默认已选中最近一次；可多选、全选后确认。")
	return b.String()
}

func cancelKeyboard(sess Session) map[string]any {
	rows := make([][]map[string]string, 0, len(sess.Records)+2)
	loc := sess.Loc
	if loc == nil {
		loc = time.Local
	}
	for i, rec := range sess.Records {
		mark := "☐"
		if sess.Selected != nil && sess.Selected[i] {
			mark = "✅"
		}
		text := fmt.Sprintf("%s %d. %s %s %s", mark, i+1, kindIcon(rec.Kind), kindDisplayName(rec.Kind), rec.At(loc).Format("15:04:05"))
		rows = append(rows, []map[string]string{{"text": text, "callback_data": "att:" + sess.Token + ":t" + strconv.Itoa(i)}})
	}
	rows = append(rows, []map[string]string{
		{"text": "选择最近一次", "callback_data": "att:" + sess.Token + ":last"},
		{"text": "全选/取消全选", "callback_data": "att:" + sess.Token + ":all"},
	})
	rows = append(rows, []map[string]string{
		{"text": "确认取消", "callback_data": "att:" + sess.Token + ":ok"},
		{"text": "关闭", "callback_data": "att:" + sess.Token + ":cancel"},
	})
	return map[string]any{"inline_keyboard": rows}
}

func selectedCancelRecords(sess Session) []AttendanceRecord {
	selected := make([]AttendanceRecord, 0, len(sess.Selected))
	for i, rec := range sess.Records {
		if sess.Selected != nil && sess.Selected[i] {
			selected = append(selected, rec)
		}
	}
	return selected
}

func cancelDoneText(sess Session, selected []AttendanceRecord, deleted int) string {
	loc := sess.Loc
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	b.WriteString(sess.MentionHTML)
	b.WriteString(fmt.Sprintf(" 已取消 %d 条打卡记录", deleted))
	for _, rec := range selected {
		b.WriteString("\n")
		b.WriteString(kindIcon(rec.Kind))
		b.WriteString(" ")
		b.WriteString(kindDisplayName(rec.Kind))
		b.WriteString(" ")
		b.WriteString(rec.At(loc).Format("2006-01-02 15:04:05"))
	}
	return b.String()
}

func (b *Bot) formatAttendanceFeedback(mentionHTML, kind string, t time.Time) string {
	text := fmt.Sprintf("%s %s 打卡%s完成 ✅\n时间：%s", mentionHTML, kindIcon(kind), kindDisplayName(kind), t.Format("2006-01-02 15:04:05"))
	if line := b.motivationLine(kind); line != "" {
		text += "\n" + line
	}
	return text
}

func (b *Bot) formatAttendanceFeedbackEnglish(mentionHTML, kind string, t time.Time) string {
	text := fmt.Sprintf("%s %s %s completed ✅\nTime: %s", mentionHTML, kindIcon(kind), kindDisplayName(kind), t.Format("2006-01-02 15:04:05"))
	if line := b.motivationLineEnglish(kind); line != "" {
		text += "\n" + line
	}
	return text
}

func (b *Bot) motivationLine(kind string) string {
	return b.motivationLineForLanguage(kind, randomMotivationLanguage(), false)
}

func (b *Bot) motivationLineEnglish(kind string) string {
	return b.motivationLineForLanguage(kind, "en", true)
}

func (b *Bot) motivationLineForLanguage(kind string, lang string, englishFixed bool) string {
	if !b.cfg.MotivationEnabled {
		return ""
	}
	phrase := ""
	if b.cfg.MotivationRemoteEnabled {
		phrase = b.fetchBestRemoteMotivation(kind, lang)
	}
	if phrase == "" {
		phrase = localMotivationFallback(kind, lang)
	}

	parts := []string{}
	if phrase != "" {
		parts = append(parts, "☀️ "+html.EscapeString(phrase))
	}
	if englishFixed {
		if fixed := fixedMotivationClosingEN(kind); fixed != "" {
			parts = append(parts, html.EscapeString(fixed))
		}
	} else if fixed := fixedMotivationClosing(kind); fixed != "" {
		parts = append(parts, html.EscapeString(fixed))
	}
	return strings.Join(parts, "\n")
}

func randomMotivationLanguage() string {
	return randomString([]string{"zh", "ja", "en"})
}

func fixedMotivationClosingEN(kind string) string {
	switch kind {
	case "in":
		return "Another energetic day. Thank you for your dedicated work."
	case "out":
		return "Another hard-working day. Thank you for your effort today."
	case "break_start":
		return "Take a proper break and come back refreshed."
	case "break_end":
		return "Welcome back. Let's get focused again."
	default:
		return ""
	}
}

func fixedMotivationClosing(kind string) string {
	switch kind {
	case "in":
		return "又是元气满满的一天，感谢您的认真工作。"
	case "out":
		return "又是辛苦的一天，感谢您的辛勤付出。"
	default:
		return ""
	}
}

func (b *Bot) fetchBestRemoteMotivation(kind, lang string) string {
	count := b.cfg.MotivationCandidateCount
	if count <= 0 {
		count = 8
	}
	if count > 20 {
		count = 20
	}
	timeout := b.cfg.MotivationTimeoutSeconds
	if timeout <= 0 {
		timeout = 3
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	urls := motivationRequestURLs(count, lang)
	ch := make(chan []string, len(urls))
	var wg sync.WaitGroup

	for _, apiURL := range urls {
		apiURL := apiURL
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidates := b.fetchMotivationCandidates(client, apiURL, lang)
			if len(candidates) > 0 {
				ch <- candidates
			}
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	all := make([]string, 0, count)
	seen := map[string]bool{}
	for candidates := range ch {
		for _, c := range candidates {
			c = sanitizeMotivation(c, lang)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			all = append(all, c)
		}
	}

	phrase := chooseBestMotivation(all, kind, lang)
	if phrase == "" && b.cfg.Debug {
		log.Printf("在线激励语没有可用候选 lang=%s，已使用本地兜底", lang)
	}
	return phrase
}

func motivationRequestURLs(count int, lang string) []string {
	if count <= 0 {
		count = 8
	}
	urls := make([]string, 0, count)
	for i := 0; i < count; i++ {
		cacheKey := fmt.Sprintf("_=%s_%d", randomToken(), i)
		switch lang {
		case "ja":
			// 日语候选源。第一个接口返回 meigen 字段；第二个作为补充，过滤器只会接受日语内容。
			if i%2 == 0 {
				urls = append(urls, "https://meigen.doodlenote.net/api/json.php?"+cacheKey)
			} else {
				urls = append(urls, "https://v1.hitokoto.cn/?encode=json&c=a&c=b&c=c&min_length=6&max_length=60&"+cacheKey)
			}
		case "en":
			// 英语候选源。多个源并行请求，任何一个失败都不会影响打卡。
			switch i % 3 {
			case 0:
				urls = append(urls, "https://api.quotable.io/random?tags=inspirational|success&maxLength=120&"+cacheKey)
			case 1:
				urls = append(urls, "https://zenquotes.io/api/random?"+cacheKey)
			default:
				urls = append(urls, "https://api.adviceslip.com/advice?"+cacheKey)
			}
		default:
			base := "https://v1.hitokoto.cn/?encode=json&c=k&c=f&c=d&min_length=8&max_length=42"
			urls = append(urls, base+"&"+cacheKey)
		}
	}
	return urls
}

func (b *Bot) fetchMotivationCandidates(client *http.Client, apiURL string, lang string) []string {
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "telegram-attendance-bot/"+version)
	req.Header.Set("Accept", "application/json,text/plain;q=0.8,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		if b.cfg.Debug {
			log.Printf("获取在线激励语失败: %v", err)
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if b.cfg.Debug {
			log.Printf("获取在线激励语 HTTP %d", resp.StatusCode)
		}
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}
	return parseMotivationCandidates(body, lang)
}

func parseMotivationCandidates(body []byte, lang string) []string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		out := []string{}
		collectMotivationStrings(v, &out)
		return cleanMotivationCandidates(out, lang)
	}
	return cleanMotivationCandidates([]string{string(body)}, lang)
}

func collectMotivationStrings(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := strings.ToLower(strings.TrimSpace(k))
			if motivationTextKey(key) {
				switch y := val.(type) {
				case string:
					*out = append(*out, y)
				case []any, map[string]any:
					collectMotivationStrings(y, out)
				}
				continue
			}
			switch val.(type) {
			case []any, map[string]any:
				collectMotivationStrings(val, out)
			}
		}
	case []any:
		for _, item := range x {
			collectMotivationStrings(item, out)
		}
	}
}

func motivationTextKey(key string) bool {
	switch key {
	case "hitokoto", "text", "content", "sentence", "note", "quote", "q", "advice", "meigen":
		return true
	default:
		return false
	}
}

func cleanMotivationCandidates(items []string, lang string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		s := sanitizeMotivation(item, lang)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func sanitizeMotivation(s string, lang string) string {
	s = html.UnescapeString(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'“”‘’` ")
	s = stripMotivationAuthor(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "http://") || strings.Contains(s, "https://") || strings.Contains(s, "<") || strings.Contains(s, ">") {
		return ""
	}

	runes := []rune(s)
	if len(runes) < 4 || len(runes) > 120 {
		return ""
	}
	if !motivationLanguageMatched(s, lang) {
		return ""
	}
	if containsBlockedMotivationWord(s) {
		return ""
	}
	return s
}

func stripMotivationAuthor(s string) string {
	seps := []string{"——", "--", " - ", " — ", " | "}
	for _, sep := range seps {
		if idx := strings.Index(s, sep); idx > 0 {
			left := strings.TrimSpace(s[:idx])
			// 作者名通常在分隔符后面，保留前半句即可。
			if left != "" {
				return strings.Trim(left, "\"'“”‘’` ")
			}
		}
	}
	return s
}

func motivationLanguageMatched(s, lang string) bool {
	switch lang {
	case "ja":
		return containsJapaneseKana(s)
	case "en":
		return containsLatinLetter(s) && !containsCJK(s) && !containsJapaneseKana(s)
	default:
		return containsCJK(s) && !containsJapaneseKana(s)
	}
}

func containsJapaneseKana(s string) bool {
	for _, r := range s {
		if (r >= '\u3040' && r <= '\u309f') || (r >= '\u30a0' && r <= '\u30ff') {
			return true
		}
	}
	return false
}

func containsLatinLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func containsBlockedMotivationWord(s string) bool {
	badWords := []string{
		"自杀", "死亡", "去死", "绝望", "废物", "垃圾", "失败者", "孤独", "寂寞", "悲伤", "痛苦", "仇恨", "杀人", "色情", "赌博",
		"死", "殺", "自殺", "絶望", "憎", "孤独", "悲しい", "苦しい", "ギャンブル",
		"suicide", "death", "die", "kill", "hate", "despair", "trash", "loser", "lonely", "gamble", "porn", "violence",
	}
	lower := strings.ToLower(s)
	for _, bad := range badWords {
		if strings.Contains(lower, strings.ToLower(bad)) {
			return true
		}
	}
	return false
}

type scoredMotivation struct {
	Text  string
	Score int
}

func chooseBestMotivation(candidates []string, kind string, lang string) string {
	if len(candidates) == 0 {
		return ""
	}
	scored := make([]scoredMotivation, 0, len(candidates))
	for _, c := range candidates {
		score := motivationScore(c, kind, lang)
		if score >= 4 {
			scored = append(scored, scoredMotivation{Text: c, Score: score})
		}
	}
	if len(scored) == 0 {
		return ""
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return len([]rune(scored[i].Text)) < len([]rune(scored[j].Text))
		}
		return scored[i].Score > scored[j].Score
	})
	limit := 5
	if len(scored) < limit {
		limit = len(scored)
	}
	top := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		top = append(top, scored[i].Text)
	}
	return randomString(top)
}

func motivationScore(s, kind string, lang string) int {
	switch lang {
	case "ja":
		return motivationScoreJA(s, kind)
	case "en":
		return motivationScoreEN(s, kind)
	default:
		return motivationScoreZH(s, kind)
	}
}

func motivationScoreZH(s, kind string) int {
	runes := []rune(s)
	l := len(runes)
	score := 0
	if l >= 8 && l <= 32 {
		score += 5
	} else if l <= 42 {
		score += 3
	} else {
		score--
	}
	positiveWords := []string{"努力", "坚持", "加油", "阳光", "今天", "成长", "优秀", "行动", "前进", "热爱", "认真", "目标", "状态", "自律", "强大", "勇敢", "希望", "成功", "未来", "奋斗", "从容", "靠谱", "稳", "向上"}
	for _, w := range positiveWords {
		if strings.Contains(s, w) {
			score += 2
		}
	}
	maleFriendlyWords := []string{"兄弟", "哥们", "男人", "少年"}
	for _, w := range maleFriendlyWords {
		if strings.Contains(s, w) {
			score += 3
		}
	}
	if kind == "in" {
		for _, w := range []string{"开始", "出发", "行动", "上路", "向上", "今天", "状态", "目标"} {
			if strings.Contains(s, w) {
				score += 2
			}
		}
	} else if kind == "out" {
		for _, w := range []string{"完成", "收尾", "辛苦", "休息", "充电", "明天", "值得", "回家"} {
			if strings.Contains(s, w) {
				score += 2
			}
		}
	}
	for _, w := range []string{"爱情", "恋", "眼泪", "沉默", "遗憾", "别离", "分手", "梦里", "孤单", "逃避"} {
		if strings.Contains(s, w) {
			score -= 4
		}
	}
	punct := strings.Count(s, "，") + strings.Count(s, "。") + strings.Count(s, "！") + strings.Count(s, "？")
	if punct > 3 {
		score -= punct - 3
	}
	return score
}

func motivationScoreJA(s, kind string) int {
	l := len([]rune(s))
	score := 0
	if l >= 8 && l <= 38 {
		score += 5
	} else if l <= 70 {
		score += 3
	} else {
		score--
	}
	for _, w := range []string{"今日", "努力", "前へ", "進", "成長", "続", "頑張", "自分", "未来", "一歩", "希望", "強", "大丈夫", "挑戦", "集中"} {
		if strings.Contains(s, w) {
			score += 2
		}
	}
	if kind == "in" {
		for _, w := range []string{"始", "今日", "朝", "一歩", "前へ", "集中", "目標"} {
			if strings.Contains(s, w) {
				score += 2
			}
		}
	} else if kind == "out" {
		for _, w := range []string{"お疲れ", "休", "明日", "充電", "完了", "頑張った", "ゆっくり"} {
			if strings.Contains(s, w) {
				score += 2
			}
		}
	}
	for _, w := range []string{"恋", "涙", "孤独", "別れ", "夢", "逃げ"} {
		if strings.Contains(s, w) {
			score -= 4
		}
	}
	return score
}

func motivationScoreEN(s, kind string) int {
	words := strings.Fields(s)
	l := len(words)
	score := 0
	if l >= 5 && l <= 16 {
		score += 5
	} else if l <= 24 {
		score += 3
	} else {
		score--
	}
	lower := strings.ToLower(s)
	for _, w := range []string{"work", "strong", "keep", "focus", "progress", "today", "goal", "action", "steady", "brave", "future", "growth", "success", "discipline", "energy", "better", "rise"} {
		if strings.Contains(lower, w) {
			score += 2
		}
	}
	for _, w := range []string{"brother", "man", "champ"} {
		if strings.Contains(lower, w) {
			score += 3
		}
	}
	if kind == "in" {
		for _, w := range []string{"start", "today", "focus", "action", "goal", "morning", "rise"} {
			if strings.Contains(lower, w) {
				score += 2
			}
		}
	} else if kind == "out" {
		for _, w := range []string{"done", "rest", "recharge", "tomorrow", "proud", "finish", "completed"} {
			if strings.Contains(lower, w) {
				score += 2
			}
		}
	}
	for _, w := range []string{"love", "tears", "lonely", "regret", "breakup", "escape"} {
		if strings.Contains(lower, w) {
			score -= 4
		}
	}
	return score
}

func localMotivationFallback(kind string, lang string) string {
	switch lang {
	case "ja":
		if kind == "break_start" {
			return randomString([]string{
				"少し休んで、また良い集中に戻ろう。",
				"ひと息ついて、力を整えよう。",
			})
		}
		if kind == "break_end" {
			return randomString([]string{
				"おかえり。落ち着いて、また前へ進もう。",
				"休憩完了。もう一度集中していこう。",
			})
		}
		if kind == "out" {
			return randomString([]string{
				"お疲れさま、今日はしっかりやり切った。ゆっくり休んで明日に備えよう。",
				"今日の努力はちゃんと積み上がっている。明日も前へ進もう。",
				"よく頑張った一日だ。しっかり充電して、また強く進もう。",
			})
		}
		return randomString([]string{
			"今日も一歩ずつ前へ進もう。集中して、良い一日にしよう。",
			"新しい一日が始まる。自分のペースで、しっかり進もう。",
			"目標を見て、落ち着いて動こう。今日もきっと積み上がる。",
		})
	case "en":
		if kind == "break_start" {
			return randomString([]string{
				"Take a clean pause and come back with sharper focus.",
				"A short reset keeps the whole day stronger.",
				"Step away, recharge, and return ready to move forward.",
			})
		}
		if kind == "break_end" {
			return randomString([]string{
				"Welcome back. Reset your focus and keep the momentum going.",
				"Break complete. Lock in and continue with steady energy.",
				"You're back. Keep it steady and make the next block count.",
			})
		}
		if kind == "out" {
			return randomString([]string{
				"Great work today, brother. Recharge well and come back stronger tomorrow.",
				"You showed up and got it done. Rest well, tomorrow is another step forward.",
				"Strong finish today. Take the evening to recover and reset.",
			})
		}
		return randomString([]string{
			"Brother, keep your focus and make today count.",
			"Start strong, stay steady, and build real progress today.",
			"Lock in your energy and move one solid step forward.",
		})
	default:
		if kind == "break_start" {
			return randomString([]string{
				"短暂休息一下，回来继续保持状态。",
				"适当放松，是为了后面更稳。",
			})
		}
		if kind == "break_end" {
			return randomString([]string{
				"欢迎回来，继续稳稳推进。",
				"休息结束，重新找回节奏。",
			})
		}
		name := randomString([]string{"兄弟", "哥们", "稳住", "保持状态"})
		if kind == "out" {
			part1 := randomString([]string{"今天已经完成得很稳", "辛苦一天，收尾漂亮", "今天的努力已经落地", "认真完成的人值得点赞"})
			part2 := randomString([]string{"回去好好充电", "晚上放松一下", "明天继续向上", "把状态留给明天"})
			return fmt.Sprintf("%s，%s，%s。", name, part1, part2)
		}
		part1 := randomString([]string{"状态拉满", "目标在前", "稳稳开局", "行动起来"})
		part2 := randomString([]string{"今天继续向上", "把节奏掌握在自己手里", "认真开始就已经领先", "一步一步把事情做好"})
		return fmt.Sprintf("%s，%s，%s。", name, part1, part2)
	}
}

func randomString(items []string) string {
	if len(items) == 0 {
		return ""
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(items))))
	if err != nil {
		return items[time.Now().UnixNano()%int64(len(items))]
	}
	return items[n.Int64()]
}

func (b *Bot) chatAllowed(chatID int64) bool {
	if len(b.cfg.AllowedChatIDs) == 0 {
		return true
	}
	for _, id := range b.cfg.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

func (b *Bot) detectCancelIntent(text string) (string, bool) {
	if !exactKeywordMatched(text, b.cfg.CancelKeywords) {
		return "", false
	}
	normalizedText := normalizeKeywordText(text)
	if strings.Contains(normalizedText, "上班") || strings.Contains(normalizedText, "clock in") || strings.Contains(normalizedText, "punch in") {
		return "in", true
	}
	if strings.Contains(normalizedText, "下班") || strings.Contains(normalizedText, "clock out") || strings.Contains(normalizedText, "punch out") {
		return "out", true
	}
	if strings.Contains(normalizedText, "回来") || strings.Contains(normalizedText, "回到") || strings.Contains(normalizedText, "come from") || strings.Contains(normalizedText, "back to") {
		return "break_end", true
	}
	if strings.Contains(normalizedText, "休息") || strings.Contains(normalizedText, "break") || strings.Contains(normalizedText, "离开") {
		return "break", true
	}
	return "", true
}

func (b *Bot) detectInteractionExit(text string) bool {
	return exactKeywordMatched(text, b.cfg.InteractionExitKeywords)
}

func (b *Bot) detectInteractiveTrigger(text string) bool {
	return prefixKeywordMatched(text, b.cfg.InteractiveTriggerKeywords)
}

func (b *Bot) detectDirectAttendanceIntent(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	matches := []string{}
	if keywordMatched(text, b.cfg.ClockInKeywords) {
		matches = append(matches, "in")
	}
	if keywordMatched(text, b.cfg.ClockOutKeywords) {
		matches = append(matches, "out")
	}
	if keywordMatched(text, b.cfg.BreakStartKeywords) {
		matches = append(matches, "break_start")
	}
	if keywordMatched(text, b.cfg.BreakEndKeywords) {
		matches = append(matches, "break_end")
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func (b *Bot) detectAttendanceIntent(text string) string {
	// Backward-compatible helper for old tests/tools. New runtime separates direct
	// attendance keywords from popup trigger keywords.
	if intent := b.detectDirectAttendanceIntent(text); intent != "" {
		return intent
	}
	if b.detectInteractiveTrigger(text) {
		return "prompt"
	}
	return ""
}

func keywordMatched(text string, keywords []string) bool {
	return exactKeywordMatched(text, keywords)
}

func prefixKeywordMatched(text string, keywords []string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return false
	}
	for _, kw := range keywords {
		kw = strings.TrimSpace(strings.ToLower(kw))
		if kw == "" {
			continue
		}
		if strings.HasPrefix(text, kw) {
			return true
		}
	}
	return false
}

func exactKeywordMatched(text string, keywords []string) bool {
	normalizedText := normalizeKeywordText(text)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if normalizedText == normalizeKeywordText(kw) {
			return true
		}
	}
	return false
}

func normalizeKeywordText(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	text = strings.TrimPrefix(text, "/")
	if idx := strings.Index(text, "@"); idx >= 0 {
		text = text[:idx]
	}
	text = strings.ReplaceAll(text, "_", " ")
	text = strings.ReplaceAll(text, "-", " ")
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}

func containsCJK(s string) bool {
	for _, r := range s {
		if (r >= '\u4e00' && r <= '\u9fff') || (r >= '\u3400' && r <= '\u4dbf') {
			return true
		}
	}
	return false
}

func kindDisplayName(kind string) string {
	switch kind {
	case "in":
		return "Check in"
	case "out":
		return "Check out"
	case "break_start":
		return "Go to break"
	case "break_end":
		return "Come from break"
	default:
		return ""
	}
}

func kindIcon(kind string) string {
	switch kind {
	case "in":
		return "💻"
	case "out":
		return "🛌"
	case "break_start":
		return "☕"
	case "break_end":
		return "↩️"
	default:
		return ""
	}
}

func normalizeCommand(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	if strings.HasPrefix(text, "/") {
		text = strings.TrimPrefix(text, "/")
		if idx := strings.Index(text, "@"); idx >= 0 {
			text = text[:idx]
		}
	}
	return text
}

func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func (b *Bot) displayLabelFromUser(u User) string {
	if alias := configuredAliasForUser(b.cfg.UserAliases, u.ID, u.Username); alias != "" {
		return alias
	}
	return preferredUserLabel(u)
}

func (b *Bot) mentionPlainFromUser(u User) string {
	return b.displayLabelFromUser(u)
}

func (b *Bot) mentionHTMLFromUser(u User) string {
	name := b.displayLabelFromUser(u)
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, u.ID, html.EscapeString(name))
}

func (b *Bot) applyConfiguredAliasesToReports(reports map[int64]*UserMonthReport) {
	for _, rep := range reports {
		if alias := configuredAliasForUser(b.cfg.UserAliases, rep.UserID, rep.Username); alias != "" {
			rep.DisplayName = alias
		}
	}
}

func displayNameFromUser(u User) string {
	return buildDisplayName(u.FirstName, u.LastName, u.Username, u.ID)
}

func preferredUserLabel(u User) string {
	name := displayNameFromUser(u)
	if name == "" {
		name = fmt.Sprintf("user_%d", u.ID)
	}
	return name
}

func configuredAliasForUser(aliases map[string]string, userID int64, username string) string {
	if len(aliases) == 0 {
		return ""
	}
	keys := []string{strconv.FormatInt(userID, 10), "id:" + strconv.FormatInt(userID, 10)}
	username = strings.TrimSpace(username)
	if username != "" {
		keys = append(keys, username, "@"+username)
	}
	for _, key := range keys {
		if alias := lookupAlias(aliases, key); alias != "" {
			return alias
		}
	}
	return ""
}

func lookupAlias(aliases map[string]string, key string) string {
	want := strings.ToLower(strings.TrimSpace(key))
	if want == "" {
		return ""
	}
	for k, v := range aliases {
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func buildDisplayName(firstName, lastName, username string, userID int64) string {
	name := strings.TrimSpace(strings.TrimSpace(firstName) + " " + strings.TrimSpace(lastName))
	if name != "" {
		return name
	}
	if username != "" {
		return username
	}
	return fmt.Sprintf("user_%d", userID)
}

func safeFileName(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
		"\n", "_", "\r", "_", "\t", "_", " ", "_",
	)
	s = replacer.Replace(s)
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "._ ")
	if len([]rune(s)) > 60 {
		runes := []rune(s)
		s = string(runes[:60])
	}
	return s
}
