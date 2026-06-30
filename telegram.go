package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// =========================
// Telegram Bot API 类型
// =========================

type TelegramClient struct {
	token string
	http  *http.Client
	debug bool
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

func NewTelegramClient(token string, debug bool) *TelegramClient {
	return &TelegramClient{
		token: token,
		http:  &http.Client{Timeout: 90 * time.Second},
		debug: debug,
	}
}

func (c *TelegramClient) url(method string) string {
	return "https://api.telegram.org/bot" + c.token + "/" + method
}

func (c *TelegramClient) postJSON(method string, payload any, result any) error {
	b, _ := json.Marshal(payload)
	resp, err := c.http.Post(c.url(method), "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram API %s HTTP %d: %s", method, resp.StatusCode, string(body))
	}
	var raw struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	if !raw.OK {
		return fmt.Errorf("Telegram API %s failed: %s", method, raw.Description)
	}
	if result != nil && len(raw.Result) > 0 {
		if err := json.Unmarshal(raw.Result, result); err != nil {
			return err
		}
	}
	return nil
}

func (c *TelegramClient) DeleteWebhook() error {
	return c.postJSON("deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (c *TelegramClient) GetUpdates(offset int64, timeout int) ([]Update, error) {
	var updates []Update
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}
	err := c.postJSON("getUpdates", payload, &updates)
	return updates, err
}

func (c *TelegramClient) SendMessage(chatID int64, text string, replyMarkup any) (*Message, error) {
	return c.SendMessageEx(chatID, text, replyMarkup, "", 0)
}

func (c *TelegramClient) SendMessageHTML(chatID int64, text string, replyMarkup any, replyToMessageID int) (*Message, error) {
	return c.SendMessageEx(chatID, text, replyMarkup, "HTML", replyToMessageID)
}

func (c *TelegramClient) SendMessageEx(chatID int64, text string, replyMarkup any, parseMode string, replyToMessageID int) (*Message, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if replyToMessageID > 0 {
		payload["reply_to_message_id"] = replyToMessageID
		payload["allow_sending_without_reply"] = true
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	var msg Message
	if err := c.postJSON("sendMessage", payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *TelegramClient) EditMessageText(chatID int64, messageID int, text string) error {
	return c.EditMessageTextEx(chatID, messageID, text, "")
}

func (c *TelegramClient) EditMessageTextHTML(chatID int64, messageID int, text string) error {
	return c.EditMessageTextEx(chatID, messageID, text, "HTML")
}

func (c *TelegramClient) EditMessageTextEx(chatID int64, messageID int, text string, parseMode string) error {
	return c.EditMessageTextExWithMarkup(chatID, messageID, text, parseMode, nil)
}

func (c *TelegramClient) EditMessageTextHTMLWithMarkup(chatID int64, messageID int, text string, replyMarkup any) error {
	return c.EditMessageTextExWithMarkup(chatID, messageID, text, "HTML", replyMarkup)
}

func (c *TelegramClient) EditMessageTextExWithMarkup(chatID int64, messageID int, text string, parseMode string, replyMarkup any) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	return c.postJSON("editMessageText", payload, nil)
}

func (c *TelegramClient) DeleteMessage(chatID int64, messageID int) error {
	return c.postJSON("deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
}

func (c *TelegramClient) AnswerCallbackQuery(callbackID, text string, showAlert bool) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		payload["text"] = text
		payload["show_alert"] = showAlert
	}
	return c.postJSON("answerCallbackQuery", payload, nil)
}

func (c *TelegramClient) SendDocument(chatID int64, filePath, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}

	partHeader := make(textproto.MIMEHeader)
	fileName := filepath.Base(filePath)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`, escapeQuotes(fileName)))
	partHeader.Set("Content-Type", detectContentType(fileName))
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.url("sendDocument"), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sendDocument HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var raw struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return err
	}
	if !raw.OK {
		return fmt.Errorf("sendDocument failed: %s", raw.Description)
	}
	return nil
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func detectContentType(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "application/octet-stream"
	}
}
