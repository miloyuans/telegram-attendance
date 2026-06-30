package main

import (
	"strings"
	"testing"
	"time"
)

func TestPendingShiftUsesCurrentTimeForRealtimeWorkHours(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	chatID := int64(-1001)
	userID := int64(7)
	inAt := time.Date(2026, 6, 30, 9, 0, 0, 0, loc)
	now := time.Date(2026, 6, 30, 15, 30, 0, 0, loc)
	records := []AttendanceRecord{
		testRecord(chatID, userID, "in", inAt),
		testRecord(chatID, userID, "break_start", time.Date(2026, 6, 30, 12, 0, 0, 0, loc)),
		testRecord(chatID, userID, "break_end", time.Date(2026, 6, 30, 13, 0, 0, 0, loc)),
	}

	reports := BuildReports(records, chatID, loc, now)
	rep := reports[userID]
	if rep == nil {
		t.Fatal("missing user report")
	}
	wantWork := 5*time.Hour + 30*time.Minute
	if rep.TotalDuration != wantWork {
		t.Fatalf("TotalDuration = %v, want %v", rep.TotalDuration, wantWork)
	}
	if rep.TotalBreakDuration != time.Hour {
		t.Fatalf("TotalBreakDuration = %v, want 1h", rep.TotalBreakDuration)
	}
	day := rep.Days["2026-06-30"]
	if day == nil || len(day.Items) != 1 {
		t.Fatalf("unexpected day report: %#v", day)
	}
	item := day.Items[0]
	if item.Status != "pending" || !item.IsRealtime || item.OnBreak {
		t.Fatalf("unexpected live item state: status=%s realtime=%v onBreak=%v", item.Status, item.IsRealtime, item.OnBreak)
	}
	cell, _ := dayCellTextLang(day, startOfDay(inAt), rep, loc, "en")
	for _, want := range []string{"In progress: calculated to current time", "Break intervals 12:00-13:00 1h", "Work hours 5h 30m"} {
		if !strings.Contains(cell, want) {
			t.Fatalf("cell missing %q:\n%s", want, cell)
		}
	}
}

func TestPendingShiftOnBreakFreezesWorkHoursAtBreakStart(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	chatID := int64(-1002)
	userID := int64(8)
	inAt := time.Date(2026, 6, 30, 9, 0, 0, 0, loc)
	now := time.Date(2026, 6, 30, 15, 0, 0, 0, loc)
	records := []AttendanceRecord{
		testRecord(chatID, userID, "in", inAt),
		testRecord(chatID, userID, "break_start", time.Date(2026, 6, 30, 12, 0, 0, 0, loc)),
	}

	reports := BuildReports(records, chatID, loc, now)
	rep := reports[userID]
	if rep == nil {
		t.Fatal("missing user report")
	}
	if rep.TotalDuration != 3*time.Hour {
		t.Fatalf("TotalDuration = %v, want 3h", rep.TotalDuration)
	}
	if rep.TotalBreakDuration != 3*time.Hour {
		t.Fatalf("TotalBreakDuration = %v, want 3h", rep.TotalBreakDuration)
	}
	day := rep.Days["2026-06-30"]
	item := day.Items[0]
	if !item.OnBreak {
		t.Fatalf("expected item to be on break")
	}
	cell, _ := dayCellTextLang(day, startOfDay(inAt), rep, loc, "en")
	for _, want := range []string{"currently on break", "Break intervals 12:00-Now 3h", "Work hours 3h"} {
		if !strings.Contains(cell, want) {
			t.Fatalf("cell missing %q:\n%s", want, cell)
		}
	}
}
