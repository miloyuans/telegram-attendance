package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnglishSummaryGeneratesHTMLAndXLSX(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	records := []AttendanceRecord{
		{ChatID: -1001, UserID: 1, DisplayName: "Alice", Kind: "in", Time: time.Date(2026, 6, 1, 9, 0, 0, 0, loc).Format(time.RFC3339), Unix: time.Date(2026, 6, 1, 9, 0, 0, 0, loc).Unix()},
		{ChatID: -1001, UserID: 1, DisplayName: "Alice", Kind: "break_start", Time: time.Date(2026, 6, 1, 12, 0, 0, 0, loc).Format(time.RFC3339), Unix: time.Date(2026, 6, 1, 12, 0, 0, 0, loc).Unix()},
		{ChatID: -1001, UserID: 1, DisplayName: "Alice", Kind: "break_end", Time: time.Date(2026, 6, 1, 13, 0, 0, 0, loc).Format(time.RFC3339), Unix: time.Date(2026, 6, 1, 13, 0, 0, 0, loc).Unix()},
		{ChatID: -1001, UserID: 1, DisplayName: "Alice", Kind: "out", Time: time.Date(2026, 6, 1, 18, 0, 0, 0, loc).Format(time.RFC3339), Unix: time.Date(2026, 6, 1, 18, 0, 0, 0, loc).Unix()},
	}
	reports := BuildReportsForFullMonth(records, -1001, loc, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), time.Date(2026, 6, 30, 15, 0, 0, 0, loc))
	dir := t.TempDir()
	xlsx, err := GenerateSummaryXLSXForMonthLang(reports, loc, dir, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), "en")
	if err != nil {
		t.Fatalf("GenerateSummaryXLSXForMonthLang: %v", err)
	}
	if filepath.Base(xlsx) != "All_2026-06_Attendance_Summary.xlsx" {
		t.Fatalf("unexpected xlsx name: %s", filepath.Base(xlsx))
	}
	htmlPath, err := GenerateSummaryHTMLForMonth(reports, loc, dir, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), time.Date(2026, 6, 30, 15, 0, 0, 0, loc), "en")
	if err != nil {
		t.Fatalf("GenerateSummaryHTMLForMonth: %v", err)
	}
	body, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"All Members Attendance Summary", "Monthly work hours", "Break deducted 1h", "Work hours 8h"} {
		if !strings.Contains(text, want) {
			t.Fatalf("html missing %q: %s", want, text)
		}
	}
}
