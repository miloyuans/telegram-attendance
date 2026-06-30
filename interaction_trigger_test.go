package main

import "testing"

func TestInteractiveTriggerPrefixOnly(t *testing.T) {
	bot := NewBot(Config{InteractiveTriggerKeywords: []string{"/start", "start"}}, nil)

	shouldMatch := []string{"/start", "/Start", "Start", "start now", "/start attendance", "/start@attendance_bot"}
	for _, text := range shouldMatch {
		if !bot.detectInteractiveTrigger(text) {
			t.Fatalf("expected %q to match interactive trigger", text)
		}
	}

	shouldNotMatch := []string{"hello start", "please /start", "check in", "打卡", "1"}
	for _, text := range shouldNotMatch {
		if bot.detectInteractiveTrigger(text) {
			t.Fatalf("expected %q not to match interactive trigger", text)
		}
	}
}

func TestDirectAttendanceSeparatedFromInteractiveTrigger(t *testing.T) {
	bot := NewBot(Config{
		ClockInKeywords:            []string{"check in"},
		InteractiveTriggerKeywords: []string{"/start", "start"},
	}, nil)

	if got := bot.detectDirectAttendanceIntent("check in"); got != "in" {
		t.Fatalf("expected check in direct intent, got %q", got)
	}
	if bot.detectInteractiveTrigger("check in") {
		t.Fatalf("check in must not open the interactive popup unless it is configured as a trigger")
	}
}

func TestInteractionExitExactOnly(t *testing.T) {
	bot := NewBot(Config{InteractionExitKeywords: []string{"exit", "quit", "clos", "closure"}}, nil)
	for _, text := range []string{"exit", "EXIT", "quit", "clos", "closure"} {
		if !bot.detectInteractionExit(text) {
			t.Fatalf("expected %q to close interaction", text)
		}
	}
	for _, text := range []string{"please exit", "exit now", "disclosure"} {
		if bot.detectInteractionExit(text) {
			t.Fatalf("expected %q not to close interaction", text)
		}
	}
}
