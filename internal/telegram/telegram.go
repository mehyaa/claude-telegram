// Package telegram is the part of the Telegram Bot API that ctg uses:
// long polling, plain-text messages with inline keyboards, and edits.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPI is the Bot API root.
const DefaultAPI = "https://api.telegram.org"

// MaxText is the longest message text Telegram accepts, in UTF-16 units.
const MaxText = 4096

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	Chat      Chat        `json:"chat"`
	From      *User       `json:"from,omitempty"`
	Date      int64       `json:"date"`
	Text      string      `json:"text,omitempty"`
	Caption   string      `json:"caption,omitempty"`
	Document  *Document   `json:"document,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
}

// Document is a file sent to the bot (Q75).
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

// PhotoSize is one size of a photo. The last is the largest.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size,omitempty"`
}

// File is a file the bot can download, from getFile.
type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// Error is an error reply from the Bot API.
type Error struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// Client calls the Bot API for one bot.
type Client struct {
	root  string // API root, without the token
	token string
	base  string // API root plus "/bot<token>": never log it
	http  *http.Client
}

// New returns a client for the bot with this token. api is the API root,
// DefaultAPI outside tests.
func New(api, token string) *Client {
	root := strings.TrimRight(api, "/")
	return &Client{
		root:  root,
		token: token,
		base:  root + "/bot" + token,
		http:  &http.Client{Timeout: 75 * time.Second},
	}
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A *url.Error quotes the URL, and the URL holds the token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d with unreadable body: %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		return &Error{Method: method, Code: r.ErrorCode, Description: r.Description, RetryAfter: r.Parameters.RetryAfter}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(r.Result, result)
}

// GetUpdates long-polls for messages and button taps, waiting up to timeout
// seconds. Updates before offset are confirmed and won't come back.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// ForceReply makes Telegram open the reply field (Q54). Inline keyboards
// can't hold an input.
type ForceReply struct {
	ForceReply  bool   `json:"force_reply"`
	Placeholder string `json:"input_field_placeholder,omitempty"`
}

type sendParams struct {
	ChatID      int64  `json:"chat_id"`
	MessageID   int64  `json:"message_id,omitempty"`
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	ReplyMarkup any    `json:"reply_markup,omitempty"`
	LinkPreview struct {
		Disabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

func newSendParams(chatID, messageID int64, text string, kb *InlineKeyboardMarkup) sendParams {
	p := sendParams{ChatID: chatID, MessageID: messageID, Text: clipUTF16(text, MaxText)}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	p.LinkPreview.Disabled = true
	return p
}

// SendForceReply sends text that opens the reply field.
func (c *Client) SendForceReply(ctx context.Context, chatID int64, text, placeholder string) (Message, error) {
	var m Message
	p := newSendParams(chatID, 0, text, nil)
	p.ReplyMarkup = ForceReply{ForceReply: true, Placeholder: placeholder}
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}

// SendMessage sends plain text, with buttons when kb isn't nil.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) (Message, error) {
	return c.SendMessageMode(ctx, chatID, text, "", kb)
}

// SendMessageMode is SendMessage with a parse mode, such as "HTML". The
// caller keeps HTML text short enough not to be clipped.
func (c *Client) SendMessageMode(ctx context.Context, chatID int64, text, mode string, kb *InlineKeyboardMarkup) (Message, error) {
	var m Message
	p := newSendParams(chatID, 0, text, kb)
	p.ParseMode = mode
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}

// EditMessageText replaces a message's text and buttons. A nil kb removes
// the buttons. Editing a message to what it already says is not an error.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error {
	return c.EditMessageTextMode(ctx, chatID, messageID, text, "", kb)
}

// EditMessageTextMode is EditMessageText with a parse mode.
func (c *Client) EditMessageTextMode(ctx context.Context, chatID, messageID int64, text, mode string, kb *InlineKeyboardMarkup) error {
	p := newSendParams(chatID, messageID, text, kb)
	p.ParseMode = mode
	err := c.call(ctx, "editMessageText", p, nil)
	var te *Error
	if errors.As(err, &te) && strings.Contains(te.Description, "message is not modified") {
		return nil
	}
	return err
}

// DeleteMessage deletes a message, the owner's own included: bots may
// delete incoming messages in private chats (Q27).
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// AnswerCallbackQuery stops the button's spinner, showing text as a toast
// when it isn't empty.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// SetMyCommands sets the command menu Telegram shows for the bot.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

// GetFile asks where a file the bot was sent can be downloaded (Q75).
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

// Download reads a file path from getFile, stopping after limit bytes.
func (c *Client) Download(ctx context.Context, filePath string, limit int64) ([]byte, error) {
	u := c.root + "/file/bot" + c.token + "/" + strings.TrimPrefix(filePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// SendDocument uploads a file. caption is plain text.
func (c *Client) SendDocument(ctx context.Context, chatID int64, name string, body []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", fmt.Sprint(chatID)); err != nil {
		return err
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := w.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/sendDocument", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram sendDocument: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram sendDocument: HTTP %d", resp.StatusCode)
	}
	if !r.OK {
		return &Error{Method: "sendDocument", Description: r.Description}
	}
	return nil
}

// Clip shortens s to at most n runes, ending in "…" when it cuts.
func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// clipUTF16 shortens s to at most n UTF-16 units, the way Telegram counts
// message length, ending in "…" when it cuts.
func clipUTF16(s string, n int) string {
	units := func(r rune) int {
		if r >= 0x10000 {
			return 2
		}
		return 1
	}
	total := 0
	for _, r := range s {
		total += units(r)
	}
	if total <= n {
		return s
	}
	used := 0
	for i, r := range s {
		if used+units(r) > n-1 {
			return s[:i] + "…"
		}
		used += units(r)
	}
	return s
}
