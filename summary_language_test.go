package main

import "testing"

func TestSummaryCommandLanguageArgs(t *testing.T) {
	cases := []struct {
		text     string
		wantCmd  string
		wantLang string
	}{
		{text: "slist", wantCmd: "slist", wantLang: "en"},
		{text: "/slist", wantCmd: "slist", wantLang: "en"},
		{text: "/slist@attendance_bot", wantCmd: "slist", wantLang: "en"},
		{text: "slist zh", wantCmd: "slist", wantLang: "zh"},
		{text: "slist cn", wantCmd: "slist", wantLang: "zh"},
		{text: "/slist@attendance_bot zh", wantCmd: "slist", wantLang: "zh"},
		{text: "slist en", wantCmd: "slist", wantLang: "en"},
	}
	for _, tc := range cases {
		cmd, args := commandWithArgs(tc.text)
		if cmd != tc.wantCmd {
			t.Fatalf("%q command = %q, want %q", tc.text, cmd, tc.wantCmd)
		}
		if got := summaryLanguageFromArgs(args); got != tc.wantLang {
			t.Fatalf("%q lang = %q, want %q", tc.text, got, tc.wantLang)
		}
	}
}

func TestSummaryDefaultLanguageIndependentOfChatMode(t *testing.T) {
	cfg := defaultConfig()
	cfg.ChatAttendanceModes = map[string]string{"-1001": "simple", "-1002": "interactive", "-1003": "all"}
	bot := NewBot(cfg, nil)
	for _, text := range []string{"slist", "/slist"} {
		_, args := commandWithArgs(text)
		if got := summaryLanguageFromArgs(args); got != "en" {
			t.Fatalf("default summary language for %q = %q, want en", text, got)
		}
	}
	_ = bot
}
