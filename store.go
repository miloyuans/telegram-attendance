package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// =========================
// 打卡记录存储
// =========================

type AttendanceRecord struct {
	ChatID      int64  `json:"chat_id"`
	UserID      int64  `json:"user_id"`
	Username    string `json:"username,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"` // in / out / break_start / break_end
	Time        string `json:"time"` // RFC3339
	Unix        int64  `json:"unix"`
}

func (r AttendanceRecord) At(loc *time.Location) time.Time {
	if r.Time != "" {
		if t, err := time.Parse(time.RFC3339, r.Time); err == nil {
			return t.In(loc)
		}
	}
	return time.Unix(r.Unix, 0).In(loc)
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Append(rec AttendanceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) ReadAll(loc *time.Location) ([]AttendanceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []AttendanceRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		rec, ok := parseRecordLine(line, loc)
		if ok {
			records = append(records, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) DeleteMatching(targets []AttendanceRecord, loc *time.Location) (int, error) {
	if len(targets) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	wanted := map[string]int{}
	for _, rec := range targets {
		wanted[recordKey(rec)]++
	}

	var kept [][]byte
	deleted := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		rawLine := append([]byte(nil), scanner.Bytes()...)
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			kept = append(kept, rawLine)
			continue
		}
		rec, ok := parseRecordLine(line, loc)
		if ok {
			key := recordKey(rec)
			if wanted[key] > 0 {
				wanted[key]--
				deleted++
				continue
			}
		}
		kept = append(kept, rawLine)
	}
	if err := scanner.Err(); err != nil {
		return deleted, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return deleted, err
	}
	tmpPath := s.path + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return deleted, err
	}
	for _, line := range kept {
		if _, err := out.Write(line); err != nil {
			_ = out.Close()
			return deleted, err
		}
		if _, err := out.Write([]byte("\n")); err != nil {
			_ = out.Close()
			return deleted, err
		}
	}
	if err := out.Close(); err != nil {
		return deleted, err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func recordKey(rec AttendanceRecord) string {
	return fmt.Sprintf("%d|%d|%s|%d|%s", rec.ChatID, rec.UserID, rec.Kind, rec.Unix, rec.Time)
}

func parseRecordLine(line []byte, loc *time.Location) (AttendanceRecord, bool) {
	var rec AttendanceRecord
	if err := json.Unmarshal(line, &rec); err == nil && rec.UserID != 0 {
		rec.Kind = normalizeKind(rec.Kind)
		if rec.Kind == "" {
			// 兼容旧字段
			var m map[string]any
			_ = json.Unmarshal(line, &m)
			rec.Kind = normalizeKind(firstString(m, "action", "type", "direction", "check_type"))
		}
		if rec.DisplayName == "" {
			rec.DisplayName = buildDisplayName(rec.FirstName, rec.LastName, rec.Username, rec.UserID)
		}
		if rec.Time == "" {
			var m map[string]any
			_ = json.Unmarshal(line, &m)
			rec.Time = firstString(m, "timestamp", "created_at", "at", "datetime")
			if rec.Time == "" && rec.Unix == 0 {
				rec.Unix = firstInt64(m, "time_unix", "created_unix", "unix")
			}
		}
		if isAttendanceKind(rec.Kind) {
			return rec, true
		}
	}

	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return AttendanceRecord{}, false
	}
	rec.ChatID = firstInt64(m, "chat_id", "chatID")
	rec.UserID = firstInt64(m, "user_id", "userID")
	rec.Username = firstString(m, "username")
	rec.FirstName = firstString(m, "first_name", "firstName")
	rec.LastName = firstString(m, "last_name", "lastName")
	rec.DisplayName = firstString(m, "display_name", "displayName", "name")
	rec.Kind = normalizeKind(firstString(m, "kind", "action", "type", "direction", "check_type"))
	rec.Time = firstString(m, "time", "timestamp", "created_at", "at", "datetime")
	rec.Unix = firstInt64(m, "unix", "time_unix", "created_unix")
	if rec.DisplayName == "" {
		rec.DisplayName = buildDisplayName(rec.FirstName, rec.LastName, rec.Username, rec.UserID)
	}
	if rec.UserID == 0 || rec.Kind == "" || (rec.Time == "" && rec.Unix == 0) {
		return AttendanceRecord{}, false
	}
	_ = loc
	return rec, true
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64:
				return strconv.FormatInt(int64(x), 10)
			}
		}
	}
	return ""
}

func firstInt64(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case float64:
				return int64(x)
			case int64:
				return x
			case string:
				if n, err := strconv.ParseInt(x, 10, 64); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func normalizeKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "in", "clock_in", "punch_in", "check_in", "checkin", "start", "上班", "打卡上班":
		return "in"
	case "out", "clock_out", "punch_out", "check_out", "checkout", "end", "下班", "打卡下班":
		return "out"
	case "break_start", "go_to_break", "start_break", "on_break", "leave_work", "away", "休息", "开始休息", "离开工作":
		return "break_start"
	case "break_end", "come_from_break", "end_break", "back_to_work", "return_work", "回来工作", "回到工作", "结束休息":
		return "break_end"
	default:
		return ""
	}
}

func isAttendanceKind(kind string) bool {
	switch kind {
	case "in", "out", "break_start", "break_end":
		return true
	default:
		return false
	}
}
