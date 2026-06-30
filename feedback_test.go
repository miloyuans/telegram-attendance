package main

import (
	"strings"
	"testing"
	"time"
)

func TestInteractiveEnglishFeedbackHasNoMotivationOrSlogan(t *testing.T) {
	bot := NewBot(Config{MotivationEnabled: true, MotivationRemoteEnabled: false}, time.UTC)
	text := bot.formatAttendanceFeedbackEnglish("Milo Mi", "in", time.Date(2026, 6, 30, 13, 0, 7, 0, time.UTC))

	want := "Milo Mi 💻 Check in completed ✅\nTime: 2026-06-30 13:00:07"
	if text != want {
		t.Fatalf("unexpected interactive feedback:\nwant: %q\n got: %q", want, text)
	}
	for _, forbidden := range []string{"Brother", "Another energetic day", "Thank you", "🌟", "☀️"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("interactive feedback should not contain %q: %s", forbidden, text)
		}
	}
}

func TestSimpleFeedbackKeepsMotivation(t *testing.T) {
	bot := NewBot(Config{MotivationEnabled: true, MotivationRemoteEnabled: false}, time.UTC)
	text := bot.formatAttendanceFeedback("Milo Mi", "in", time.Date(2026, 6, 30, 13, 0, 7, 0, time.UTC))
	if !strings.Contains(text, "\n") {
		t.Fatalf("simple feedback should keep motivation/fixed line, got: %q", text)
	}
}
