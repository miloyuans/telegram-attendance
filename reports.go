package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// =========================
// 统计逻辑
// =========================

type BreakItem struct {
	Start    *AttendanceRecord
	End      *AttendanceRecord
	Duration time.Duration
}

type ShiftItem struct {
	Date          time.Time
	In            *AttendanceRecord
	Out           *AttendanceRecord
	Status        string // normal / missing_out / missing_in / pending
	Note          string
	GrossDuration time.Duration
	BreakDuration time.Duration
	Duration      time.Duration // actual paid/working time after subtracting break intervals
	Breaks        []BreakItem
}

type DayReport struct {
	Date          time.Time
	Items         []ShiftItem
	NormalCount   int
	AbnormalCount int
	TotalDuration time.Duration
}

type UserMonthReport struct {
	ChatID        int64
	UserID        int64
	DisplayName   string
	Username      string
	MonthStart    time.Time
	DisplayEnd    time.Time
	FinalizedEnd  time.Time
	GeneratedAt   time.Time
	Days          map[string]*DayReport
	NormalCount   int
	AbnormalCount int
	TotalDuration time.Duration
}

func BuildReports(records []AttendanceRecord, chatID int64, loc *time.Location, now time.Time) map[int64]*UserMonthReport {
	now = now.In(loc)
	monthStart := startOfMonth(now)
	monthEnd := monthStart.AddDate(0, 1, -1)
	displayEnd := startOfDay(now)
	if displayEnd.After(monthEnd) {
		displayEnd = monthEnd
	}
	// 手动 list/slist 默认只结算到昨天，避免当天夜班/进行中班次被提前判异常。
	finalizedEnd := startOfDay(now).AddDate(0, 0, -1)
	return buildReportsForWindow(records, chatID, loc, monthStart, displayEnd, finalizedEnd, now)
}

func BuildReportsForFullMonth(records []AttendanceRecord, chatID int64, loc *time.Location, month time.Time, generatedAt time.Time) map[int64]*UserMonthReport {
	monthStart := startOfMonth(month.In(loc))
	monthEnd := monthStart.AddDate(0, 1, -1)
	return buildReportsForWindow(records, chatID, loc, monthStart, monthEnd, monthEnd, generatedAt.In(loc))
}

func buildReportsForWindow(records []AttendanceRecord, chatID int64, loc *time.Location, monthStart, displayEnd, finalizedEnd, generatedAt time.Time) map[int64]*UserMonthReport {
	monthStart = startOfMonth(monthStart.In(loc))
	monthEnd := monthStart.AddDate(0, 1, -1)
	displayEnd = startOfDay(displayEnd.In(loc))
	finalizedEnd = startOfDay(finalizedEnd.In(loc))
	generatedAt = generatedAt.In(loc)
	if displayEnd.After(monthEnd) {
		displayEnd = monthEnd
	}
	if displayEnd.Before(monthStart) {
		displayEnd = monthStart.AddDate(0, 0, -1)
	}
	if finalizedEnd.After(monthEnd) {
		finalizedEnd = monthEnd
	}
	if finalizedEnd.Before(monthStart) {
		finalizedEnd = monthStart.AddDate(0, 0, -1)
	}

	byUser := map[int64][]AttendanceRecord{}
	userMeta := map[int64]AttendanceRecord{}
	pairStart := monthStart.AddDate(0, 0, -1)
	// 多向后取一天，确保月末夜班在次日下班时仍能正确归属到上班当天。
	pairEnd := displayEnd.AddDate(0, 0, 1).Add(24*time.Hour - time.Nanosecond)

	for _, rec := range records {
		if rec.ChatID != chatID || rec.UserID == 0 {
			continue
		}
		at := rec.At(loc)
		if at.Before(pairStart) || at.After(pairEnd) {
			continue
		}
		byUser[rec.UserID] = append(byUser[rec.UserID], rec)
		userMeta[rec.UserID] = rec
	}

	reports := map[int64]*UserMonthReport{}
	for userID, recs := range byUser {
		sort.Slice(recs, func(i, j int) bool { return recs[i].At(loc).Before(recs[j].At(loc)) })
		meta := userMeta[userID]
		rep := &UserMonthReport{
			ChatID:       chatID,
			UserID:       userID,
			DisplayName:  buildDisplayName(meta.FirstName, meta.LastName, meta.Username, meta.UserID),
			Username:     meta.Username,
			MonthStart:   monthStart,
			DisplayEnd:   displayEnd,
			FinalizedEnd: finalizedEnd,
			GeneratedAt:  generatedAt,
			Days:         map[string]*DayReport{},
		}
		if rep.DisplayName == "" {
			rep.DisplayName = buildDisplayName(meta.FirstName, meta.LastName, meta.Username, meta.UserID)
		}
		for d := monthStart; !d.After(monthEnd); d = d.AddDate(0, 0, 1) {
			key := dateKey(d)
			rep.Days[key] = &DayReport{Date: d}
		}

		usedOut := map[int]bool{}

		// 先按“上班记录”生成班次。规则：
		// 1. 上班当天或次日找到下班，可以配对；
		// 2. 如果在下班前先遇到下一次上班，则当前上班缺失下班，异常；
		// 3. 异常班次不计算工时；
		// 4. 夜班工时归属到上班当天。
		for i := range recs {
			inRec := recs[i]
			if inRec.Kind != "in" {
				continue
			}
			inAt := inRec.At(loc)
			workDate := startOfDay(inAt)
			allowedEnd := workDate.AddDate(0, 0, 2) // 上班当天 00:00 + 2 天，即次日 24:00 前
			outIdx := -1

			for j := i + 1; j < len(recs); j++ {
				cand := recs[j]
				candAt := cand.At(loc)
				if !candAt.After(inAt) {
					continue
				}
				if !candAt.Before(allowedEnd) {
					break
				}
				if cand.Kind == "in" {
					break
				}
				if cand.Kind == "out" && !usedOut[j] {
					outIdx = j
					break
				}
			}

			if workDate.Before(monthStart) || workDate.After(monthEnd) {
				if outIdx >= 0 {
					usedOut[outIdx] = true
				}
				continue
			}

			item := ShiftItem{Date: workDate, In: &recs[i]}
			if outIdx >= 0 {
				usedOut[outIdx] = true
				item.Out = &recs[outIdx]
				item.Status = "normal"
				item.Note = "正常"
				outAt := recs[outIdx].At(loc)
				item.GrossDuration = outAt.Sub(inAt)
				if item.GrossDuration < 0 {
					item.Status = "missing_out"
					item.Note = "异常：下班时间早于上班时间"
					item.GrossDuration = 0
					item.Duration = 0
				} else {
					item.Breaks, item.BreakDuration = collectBreaksWithinShift(recs, inAt, outAt, loc)
					item.Duration = item.GrossDuration - item.BreakDuration
					if item.Duration < 0 {
						item.Duration = 0
					}
					if item.BreakDuration > 0 {
						item.Note = "正常：已扣除休息 " + formatDurationCN(item.BreakDuration)
					}
				}
			} else if !workDate.After(finalizedEnd) {
				item.Status = "missing_out"
				item.Note = "异常：缺失下班记录"
			} else {
				item.Status = "pending"
				item.Note = "未结算：当天或夜班可能仍在进行中"
			}
			addItem(rep, item)
		}

		// 再找没有被任何上班配对消耗的下班记录。
		// 如果它属于昨天夜班，前面的 usedOut 会把它标记掉；不会再把次日下班休息日判异常。
		for i := range recs {
			outRec := recs[i]
			if outRec.Kind != "out" || usedOut[i] {
				continue
			}
			outDate := startOfDay(outRec.At(loc))
			if outDate.Before(monthStart) || outDate.After(monthEnd) {
				continue
			}
			item := ShiftItem{Date: outDate, Out: &recs[i]}
			if !outDate.After(finalizedEnd) {
				item.Status = "missing_in"
				item.Note = "异常：缺失上班记录"
			} else {
				item.Status = "pending"
				item.Note = "未结算：仅有下班记录，等待结算"
			}
			addItem(rep, item)
		}

		reports[userID] = rep
	}
	return reports
}

func collectBreaksWithinShift(recs []AttendanceRecord, inAt, outAt time.Time, loc *time.Location) ([]BreakItem, time.Duration) {
	if !outAt.After(inAt) {
		return nil, 0
	}
	breaks := []BreakItem{}
	var active *AttendanceRecord
	for i := range recs {
		rec := recs[i]
		at := rec.At(loc)
		if at.Before(inAt) || at.Equal(inAt) || at.After(outAt) {
			continue
		}
		switch rec.Kind {
		case "break_start":
			if active == nil {
				active = &recs[i]
			}
		case "break_end":
			if active != nil {
				startAt := active.At(loc)
				endAt := at
				if startAt.Before(inAt) {
					startAt = inAt
				}
				if endAt.After(outAt) {
					endAt = outAt
				}
				if endAt.After(startAt) {
					breaks = append(breaks, BreakItem{Start: active, End: &recs[i], Duration: endAt.Sub(startAt)})
				}
				active = nil
			}
		}
	}
	if active != nil {
		startAt := active.At(loc)
		if startAt.Before(inAt) {
			startAt = inAt
		}
		if outAt.After(startAt) {
			breaks = append(breaks, BreakItem{Start: active, End: nil, Duration: outAt.Sub(startAt)})
		}
	}
	var total time.Duration
	for _, br := range breaks {
		if br.Duration > 0 {
			total += br.Duration
		}
	}
	return breaks, total
}

func addItem(rep *UserMonthReport, item ShiftItem) {
	key := dateKey(item.Date)
	day, ok := rep.Days[key]
	if !ok {
		return
	}
	day.Items = append(day.Items, item)
	if item.Status == "normal" {
		day.NormalCount++
		day.TotalDuration += item.Duration
		rep.NormalCount++
		rep.TotalDuration += item.Duration
	} else if strings.HasPrefix(item.Status, "missing") {
		day.AbnormalCount++
		rep.AbnormalCount++
	}
}

func startOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func dateKey(t time.Time) string {
	return t.Format("2006-01-02")
}

func daysInMonth(t time.Time) int {
	return startOfMonth(t).AddDate(0, 1, -1).Day()
}

func weekdayCN(t time.Time) string {
	switch t.Weekday() {
	case time.Monday:
		return "周一"
	case time.Tuesday:
		return "周二"
	case time.Wednesday:
		return "周三"
	case time.Thursday:
		return "周四"
	case time.Friday:
		return "周五"
	case time.Saturday:
		return "周六"
	default:
		return "周日"
	}
}

func formatDurationCN(d time.Duration) string {
	if d <= 0 {
		return "0小时"
	}
	mins := int(d.Minutes() + 0.5)
	h := mins / 60
	m := mins % 60
	if m == 0 {
		return fmt.Sprintf("%d小时", h)
	}
	return fmt.Sprintf("%d小时%d分钟", h, m)
}

func formatClock(rec *AttendanceRecord, loc *time.Location) string {
	if rec == nil {
		return "-"
	}
	return rec.At(loc).Format("15:04")
}

func formatClockWithDate(rec *AttendanceRecord, baseDate time.Time, loc *time.Location) string {
	if rec == nil {
		return "-"
	}
	at := rec.At(loc)
	if dateKey(at) == dateKey(baseDate) {
		return at.Format("15:04")
	}
	return at.Format("01-02 15:04")
}

// =========================
// HTML 日历报表
// =========================

func GenerateHTMLReport(rep *UserMonthReport, loc *time.Location, reportsDir string) (string, error) {
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		return "", err
	}
	fileName := safeFileName(rep.DisplayName)
	if fileName == "" {
		fileName = fmt.Sprintf("user_%d", rep.UserID)
	}
	fileName = fmt.Sprintf("%s_%s_打卡日历.html", fileName, rep.MonthStart.Format("2006-01"))
	path := filepath.Join(reportsDir, fileName)

	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>打卡日历报表</title>")
	b.WriteString(`<style>
body{margin:0;background:#f4f6f8;color:#1f2937;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",Arial,sans-serif}.wrap{max-width:1200px;margin:0 auto;padding:24px}.top{background:#111827;color:#fff;border-radius:18px;padding:22px 24px;margin-bottom:18px}.top h1{margin:0 0 10px;font-size:26px}.meta{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;color:#d1d5db;font-size:14px}.cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin:18px 0}.card{background:#fff;border-radius:16px;padding:18px;box-shadow:0 4px 14px rgba(15,23,42,.08)}.card .label{font-size:13px;color:#6b7280}.card .num{font-size:26px;font-weight:800;margin-top:6px}.calendar{background:#fff;border-radius:18px;box-shadow:0 4px 14px rgba(15,23,42,.08);overflow:hidden}.month-title{padding:18px 20px;border-bottom:1px solid #e5e7eb;font-size:20px;font-weight:800}.weekdays{display:grid;grid-template-columns:repeat(7,1fr);background:#f9fafb;border-bottom:1px solid #e5e7eb}.weekdays div{padding:12px;text-align:center;font-weight:700;color:#4b5563}.grid{display:grid;grid-template-columns:repeat(7,1fr)}.day{min-height:138px;border-right:1px solid #e5e7eb;border-bottom:1px solid #e5e7eb;padding:10px;box-sizing:border-box;background:#fff}.day:nth-child(7n){border-right:none}.empty{background:#f9fafb}.future{background:#f8fafc;color:#9ca3af}.normal{background:#ecfdf5}.abnormal{background:#fef2f2}.pending{background:#eff6ff}.day-num{font-size:15px;font-weight:800;margin-bottom:8px}.badge{display:inline-block;border-radius:999px;padding:2px 8px;font-size:12px;font-weight:700;margin-bottom:6px}.badge.normal{background:#16a34a;color:#fff}.badge.abnormal{background:#dc2626;color:#fff}.badge.pending{background:#2563eb;color:#fff}.shift{font-size:12px;line-height:1.55;margin-top:6px;padding-top:6px;border-top:1px dashed rgba(0,0,0,.12)}.muted{color:#6b7280}.legend{display:flex;gap:12px;margin:12px 0 0;font-size:13px;color:#4b5563}.dot{display:inline-block;width:12px;height:12px;border-radius:3px;margin-right:4px;vertical-align:-1px}.dot.green{background:#bbf7d0}.dot.red{background:#fecaca}.dot.blue{background:#bfdbfe}.dot.gray{background:#e5e7eb}@media(max-width:900px){.cards,.meta{grid-template-columns:1fr}.grid,.weekdays{grid-template-columns:1fr}.day{min-height:auto;border-right:none}}
</style></head><body><div class="wrap">`)

	b.WriteString("<section class=\"top\">")
	b.WriteString("<h1>个人打卡日历报表</h1>")
	b.WriteString("<div class=\"meta\">")
	b.WriteString(fmt.Sprintf("<div>用户：%s</div>", html.EscapeString(rep.DisplayName)))
	if rep.Username != "" {
		b.WriteString(fmt.Sprintf("<div>Telegram：@%s</div>", html.EscapeString(rep.Username)))
	} else {
		b.WriteString(fmt.Sprintf("<div>Telegram ID：%d</div>", rep.UserID))
	}
	b.WriteString(fmt.Sprintf("<div>报告月份：%s</div>", rep.MonthStart.Format("2006年01月")))
	b.WriteString(fmt.Sprintf("<div>生成日期：%s</div>", rep.GeneratedAt.In(loc).Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("<div>结算范围：%s 至 %s</div>", rep.MonthStart.Format("2006-01-02"), maxDateText(rep.FinalizedEnd, rep.MonthStart)))
	b.WriteString("<div>统计口径：异常不计工时；休息区间不计入总工时；夜班归属上班日期；当天默认不提前结算。</div>")
	b.WriteString("</div></section>")

	b.WriteString("<section class=\"cards\">")
	b.WriteString(metricCard("正常班次", strconv.Itoa(rep.NormalCount)))
	b.WriteString(metricCard("异常班次", strconv.Itoa(rep.AbnormalCount)))
	b.WriteString(metricCard("总工时", formatDurationCN(rep.TotalDuration)))
	b.WriteString(metricCard("日历状态", "绿=正常 ❌=异常"))
	b.WriteString("</section>")

	b.WriteString("<section class=\"calendar\">")
	b.WriteString(fmt.Sprintf("<div class=\"month-title\">%s 打卡日历</div>", rep.MonthStart.Format("2006年01月")))
	b.WriteString("<div class=\"weekdays\"><div>周一</div><div>周二</div><div>周三</div><div>周四</div><div>周五</div><div>周六</div><div>周日</div></div>")
	b.WriteString("<div class=\"grid\">")
	lead := (int(rep.MonthStart.Weekday()) + 6) % 7
	for i := 0; i < lead; i++ {
		b.WriteString("<div class=\"day empty\"></div>")
	}
	monthDays := daysInMonth(rep.MonthStart)
	for day := 1; day <= monthDays; day++ {
		d := time.Date(rep.MonthStart.Year(), rep.MonthStart.Month(), day, 0, 0, 0, 0, loc)
		reportDay := rep.Days[dateKey(d)]
		className := "day"
		badgeClass := ""
		badgeText := ""
		if d.After(rep.DisplayEnd) {
			className += " future"
		} else if reportDay != nil && reportDay.AbnormalCount > 0 {
			className += " abnormal"
			badgeClass = "abnormal"
			badgeText = "❌ 异常"
		} else if reportDay != nil && reportDay.NormalCount > 0 {
			className += " normal"
			badgeClass = "normal"
			badgeText = "正常"
		} else if d.After(rep.FinalizedEnd) && !d.After(rep.DisplayEnd) {
			className += " pending"
			badgeClass = "pending"
			badgeText = "未结算"
		}
		b.WriteString(fmt.Sprintf("<div class=\"%s\">", className))
		b.WriteString(fmt.Sprintf("<div class=\"day-num\">%d <span class=\"muted\">%s</span></div>", day, weekdayCN(d)))
		if badgeText != "" {
			b.WriteString(fmt.Sprintf("<span class=\"badge %s\">%s</span>", badgeClass, badgeText))
		}
		if reportDay != nil {
			for _, item := range reportDay.Items {
				b.WriteString(renderShiftHTML(item, loc))
			}
		}
		b.WriteString("</div>")
	}
	b.WriteString("</div>")
	b.WriteString("<div class=\"legend\" style=\"padding:0 20px 18px\"><span><i class=\"dot green\"></i>正常</span><span><i class=\"dot red\"></i>异常</span><span><i class=\"dot blue\"></i>未结算</span><span><i class=\"dot gray\"></i>无记录/未来日期</span></div>")
	b.WriteString("</section></div></body></html>")

	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func metricCard(label, value string) string {
	return fmt.Sprintf("<div class=\"card\"><div class=\"label\">%s</div><div class=\"num\">%s</div></div>", html.EscapeString(label), html.EscapeString(value))
}

func renderShiftHTML(item ShiftItem, loc *time.Location) string {
	status := item.Note
	if status == "" {
		status = item.Status
	}
	dur := "-"
	breakText := "-"
	grossText := "-"
	if item.Status == "normal" {
		dur = formatDurationCN(item.Duration)
		grossText = formatDurationCN(item.GrossDuration)
		if item.BreakDuration > 0 {
			breakText = formatDurationCN(item.BreakDuration)
		} else {
			breakText = "0小时"
		}
	}
	return fmt.Sprintf(`<div class="shift"><div>上班：%s</div><div>下班：%s</div><div>状态：%s</div><div>总时长：%s</div><div>休息扣除：%s</div><div>计入工时：%s</div></div>`,
		html.EscapeString(formatClockWithDate(item.In, item.Date, loc)),
		html.EscapeString(formatClockWithDate(item.Out, item.Date, loc)),
		html.EscapeString(status),
		html.EscapeString(grossText),
		html.EscapeString(breakText),
		html.EscapeString(dur),
	)
}

func maxDateText(finalizedEnd, monthStart time.Time) string {
	if finalizedEnd.Before(monthStart) {
		return "暂无已结算日期"
	}
	return finalizedEnd.Format("2006-01-02")
}

// =========================
// XLSX 全员汇总
// =========================

type xlsxCell struct {
	Text  string
	Style int
}

func GenerateSummaryXLSX(reports map[int64]*UserMonthReport, loc *time.Location, reportsDir string, now time.Time) (string, error) {
	return GenerateSummaryXLSXForMonth(reports, loc, reportsDir, startOfMonth(now.In(loc)))
}

func GenerateSummaryXLSXForMonth(reports map[int64]*UserMonthReport, loc *time.Location, reportsDir string, month time.Time) (string, error) {
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		return "", err
	}
	monthStart := startOfMonth(month.In(loc))
	fileName := fmt.Sprintf("全员_%s_打卡汇总.xlsx", monthStart.Format("2006-01"))
	path := filepath.Join(reportsDir, fileName)

	monthDays := daysInMonth(monthStart)
	headers := []string{"用户"}
	for day := 1; day <= monthDays; day++ {
		d := time.Date(monthStart.Year(), monthStart.Month(), day, 0, 0, 0, 0, loc)
		headers = append(headers, fmt.Sprintf("%d日\n%s", day, weekdayCN(d)))
	}
	headers = append(headers, "当月总工时")

	userIDs := make([]int64, 0, len(reports))
	for uid := range reports {
		userIDs = append(userIDs, uid)
	}
	sort.Slice(userIDs, func(i, j int) bool {
		return reports[userIDs[i]].DisplayName < reports[userIDs[j]].DisplayName
	})

	rows := [][]xlsxCell{}
	for _, uid := range userIDs {
		rep := reports[uid]
		row := []xlsxCell{{Text: rep.DisplayName, Style: 4}}
		for day := 1; day <= monthDays; day++ {
			d := time.Date(monthStart.Year(), monthStart.Month(), day, 0, 0, 0, 0, loc)
			dayReport := rep.Days[dateKey(d)]
			text, style := dayCellText(dayReport, d, rep, loc)
			row = append(row, xlsxCell{Text: text, Style: style})
		}
		row = append(row, xlsxCell{Text: formatDurationCN(rep.TotalDuration), Style: 5})
		rows = append(rows, row)
	}
	return path, writeXLSX(path, headers, rows)
}

func dayCellText(dayReport *DayReport, d time.Time, rep *UserMonthReport, loc *time.Location) (string, int) {
	if d.After(rep.DisplayEnd) {
		return "", 0
	}
	if dayReport == nil || len(dayReport.Items) == 0 {
		if d.After(rep.FinalizedEnd) && !d.After(rep.DisplayEnd) {
			return "未结算", 4
		}
		return "", 0
	}
	parts := make([]string, 0, len(dayReport.Items))
	style := 2
	for _, item := range dayReport.Items {
		status := item.Note
		if status == "" {
			status = item.Status
		}
		dur := "-"
		if item.Status == "normal" {
			dur = formatDurationCN(item.Duration)
		}
		breakLine := "休息扣除 -"
		grossLine := "总时长 -"
		if item.Status == "normal" {
			grossLine = "总时长 " + formatDurationCN(item.GrossDuration)
			breakLine = "休息扣除 " + formatDurationCN(item.BreakDuration)
		}
		parts = append(parts, fmt.Sprintf("上班 %s\n下班 %s\n%s\n%s\n%s\n计入工时 %s",
			formatClockWithDate(item.In, item.Date, loc),
			formatClockWithDate(item.Out, item.Date, loc),
			status,
			grossLine,
			breakLine,
			dur,
		))
		if strings.HasPrefix(item.Status, "missing") {
			style = 3
		} else if item.Status == "pending" && style != 3 {
			style = 4
		}
	}
	return strings.Join(parts, "\n---\n"), style
}

func writeXLSX(path string, headers []string, rows [][]xlsxCell) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML(),
		"_rels/.rels":                relsXML(),
		"xl/workbook.xml":            workbookXML(),
		"xl/_rels/workbook.xml.rels": workbookRelsXML(),
		"xl/styles.xml":              stylesXML(),
		"xl/worksheets/sheet1.xml":   sheetXML(headers, rows),
	}
	order := []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml"}
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			return err
		}
	}
	return nil
}

func contentTypesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`
}

func relsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`
}

func workbookXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="打卡汇总" sheetId="1" r:id="rId1"/></sheets>
</workbook>`
}

func workbookRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`
}

func stylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="5"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFD9EAD3"/><bgColor indexed="64"/></patternFill></fill><fill><patternFill patternType="solid"><fgColor rgb="FFF4CCCC"/><bgColor indexed="64"/></patternFill></fill><fill><patternFill patternType="solid"><fgColor rgb="FFEFEFEF"/><bgColor indexed="64"/></patternFill></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="6">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="4" borderId="0" xfId="0" applyFont="1" applyFill="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf>
<xf numFmtId="0" fontId="0" fillId="2" borderId="0" xfId="0" applyFill="1"><alignment vertical="top" wrapText="1"/></xf>
<xf numFmtId="0" fontId="0" fillId="3" borderId="0" xfId="0" applyFill="1"><alignment vertical="top" wrapText="1"/></xf>
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"><alignment vertical="top" wrapText="1"/></xf>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"><alignment vertical="center" wrapText="1"/></xf>
</cellXfs>
<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>`
}

func sheetXML(headers []string, rows [][]xlsxCell) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="18"/>`)
	b.WriteString(`<cols><col min="1" max="1" width="22" customWidth="1"/>`)
	if len(headers) > 2 {
		b.WriteString(fmt.Sprintf(`<col min="2" max="%d" width="22" customWidth="1"/>`, len(headers)-1))
	}
	b.WriteString(fmt.Sprintf(`<col min="%d" max="%d" width="16" customWidth="1"/>`, len(headers), len(headers)))
	b.WriteString(`</cols><sheetData>`)
	b.WriteString(`<row r="1" ht="34" customHeight="1">`)
	for i, h := range headers {
		b.WriteString(cellXML(1, i+1, h, 1))
	}
	b.WriteString(`</row>`)
	for r, row := range rows {
		rowNum := r + 2
		b.WriteString(fmt.Sprintf(`<row r="%d" ht="90" customHeight="1">`, rowNum))
		for c, cell := range row {
			b.WriteString(cellXML(rowNum, c+1, cell.Text, cell.Style))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func cellXML(row, col int, text string, style int) string {
	ref := columnName(col) + strconv.Itoa(row)
	if text == "" {
		return fmt.Sprintf(`<c r="%s" s="%d"/>`, ref, style)
	}
	return fmt.Sprintf(`<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlEscape(text))
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func columnName(n int) string {
	name := ""
	for n > 0 {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	return name
}
