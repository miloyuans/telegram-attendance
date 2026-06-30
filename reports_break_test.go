package main

import (
	"testing"
	"time"
)

func testRecord(chatID, userID int64, kind string, t time.Time) AttendanceRecord {
	return AttendanceRecord{ChatID: chatID, UserID: userID, DisplayName: "tester", Kind: kind, Time: t.Format(time.RFC3339), Unix: t.Unix()}
}

func TestBuildReportsDeductsBreakTime(t *testing.T) {
	loc := time.FixedZone("T", 0)
	start := time.Date(2026, 6, 10, 9, 0, 0, 0, loc)
	records := []AttendanceRecord{
		testRecord(100, 200, "in", start),
		testRecord(100, 200, "break_start", start.Add(2*time.Hour)),
		testRecord(100, 200, "break_end", start.Add(3*time.Hour+30*time.Minute)),
		testRecord(100, 200, "out", start.Add(9*time.Hour)),
	}
	reports := BuildReportsForFullMonth(records, 100, loc, start, start)
	rep := reports[200]
	if rep == nil {
		t.Fatal("missing user report")
	}
	want := 7*time.Hour + 30*time.Minute
	if rep.TotalDuration != want {
		t.Fatalf("TotalDuration mismatch: want %v got %v", want, rep.TotalDuration)
	}
}
