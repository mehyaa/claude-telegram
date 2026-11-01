// Package fake is a fake Bot API server for tests and for the faketg
// probe. Callers push the updates a phone would produce and read back what
// the bot sent.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/telegram"
)

// Token is the bot token the fake accepts.
const Token = "123456:fake-token"

// Sent is a sendMessage, editMessageText or deleteMessage call the bot
// made.
type Sent struct {
	Method      string // "sendMessage", "editMessageText" or "deleteMessage"
	ChatID      int64
	MessageID   int64 // the new message's ID, or the edited or deleted one's
	Text        string
	ParseMode   string
	Keyboard    *telegram.InlineKeyboardMarkup
	ForceReply  bool
	Placeholder string
	File        string // sendDocument filename
}

// Buttons returns the callback data of every button, keyed by label.
func (s Sent) Buttons() map[string]string {
	m := map[string]string{}
	if s.Keyboard != nil {
		for _, row := range s.Keyboard.InlineKeyboard {
			for _, b := range row {
				m[b.Text] = b.CallbackData
			}
		}
	}
	return m
}

type Server struct {
	URL   string
	close func()

	mu         sync.Mutex
	updates    []telegram.Update
	nextUpdate int64
	nextMsg    int64
	wake       chan struct{}
	files      map[string][]byte
	sent       chan Sent
	answers    chan string
}

// New starts a fake that stops when the test ends.
func New(t testing.TB) *Server {
	s := Start()
	t.Cleanup(s.Close)
	return s
}

// Start starts a fake outside a test; Close stops it.
func Start() *Server {
	s := &Server{
		nextUpdate: 1,
		nextMsg:    1000,
		wake:       make(chan struct{}),
		sent:       make(chan Sent, 256),
		answers:    make(chan string, 256),
	}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL, s.close = srv.URL, srv.Close
	return s
}

func (s *Server) Close() { s.close() }

// Sent returns the channel of the bot's messages and edits.
func (s *Server) Sent() <-chan Sent { return s.sent }

// Answers returns the channel of the toasts the bot answered taps with.
func (s *Server) Answers() <-chan string { return s.answers }

func (s *Server) push(u telegram.Update) {
	s.mu.Lock()
	u.UpdateID = s.nextUpdate
	s.nextUpdate++
	s.updates = append(s.updates, u)
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

// Send delivers text typed in the private chat chatID, dated now.
func (s *Server) Send(chatID int64, text string) {
	s.SendAt(chatID, "private", text, time.Now())
}

// SendAt delivers text from a chat of the given type with the given date.
func (s *Server) SendAt(chatID int64, chatType, text string, date time.Time) {
	s.mu.Lock()
	id := s.nextMsg
	s.nextMsg++
	s.mu.Unlock()
	s.push(telegram.Update{Message: &telegram.Message{
		MessageID: id,
		Chat:      telegram.Chat{ID: chatID, Type: chatType},
		From:      &telegram.User{ID: chatID},
		Date:      date.Unix(),
		Text:      text,
	}})
}

// File delivers a document to the private chat, and serves its bytes from getFile.
func (s *Server) File(chatID int64, fileID, name string, body []byte, caption string) {
	s.FileSized(chatID, fileID, name, int64(len(body)), body, caption)
}

// FileSized is File with an explicit size. A size over 20 MB is refused
// before the bot downloads, so body may be empty.
func (s *Server) FileSized(chatID int64, fileID, name string, size int64, body []byte, caption string) {
	s.mu.Lock()
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	if body != nil {
		s.files[fileID] = append([]byte(nil), body...)
	}
	id := s.nextMsg
	s.nextMsg++
	s.mu.Unlock()
	s.push(telegram.Update{Message: &telegram.Message{
		MessageID: id,
		Chat:      telegram.Chat{ID: chatID, Type: "private"},
		From:      &telegram.User{ID: chatID},
		Date:      time.Now().Unix(),
		Caption:   caption,
		Document:  &telegram.Document{FileID: fileID, FileName: name, FileSize: size},
	}})
}

// Tap presses the button with this callback data on message messageID.
func (s *Server) Tap(chatID, messageID int64, data string) {
	s.mu.Lock()
	id := s.nextMsg
	s.nextMsg++
	s.mu.Unlock()
	s.push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID:      fmt.Sprintf("cb%d", id),
		From:    telegram.User{ID: chatID},
		Message: &telegram.Message{MessageID: messageID, Chat: telegram.Chat{ID: chatID, Type: "private"}},
		Data:    data,
	}})
}

// Next waits for the bot's next message or edit.
func (s *Server) Next(t testing.TB) Sent {
	t.Helper()
	select {
	case m := <-s.sent:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("bot sent nothing within 3s")
		return Sent{}
	}
}

// Quiet fails the test if the bot sends anything within d.
func (s *Server) Quiet(t testing.TB, d time.Duration) {
	t.Helper()
	select {
	case m := <-s.sent:
		t.Fatalf("bot sent %s to %d: %q", m.Method, m.ChatID, m.Text)
	case <-time.After(d):
	}
}

// Answered waits for the bot to answer a button tap and returns the toast.
func (s *Server) Answered(t testing.TB) string {
	t.Helper()
	select {
	case a := <-s.answers:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("bot answered no button tap within 3s")
		return ""
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/bot"+Token+"/files/") {
		id := strings.TrimPrefix(r.URL.Path, "/file/bot"+Token+"/files/")
		s.mu.Lock()
		b := s.files[id]
		s.mu.Unlock()
		if b == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/sendDocument") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			reply(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		name := ""
		if f, hdr, err := r.FormFile("document"); err == nil {
			name = hdr.Filename
			f.Close()
		}
		chat, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		s.mu.Lock()
		id := s.nextMsg
		s.nextMsg++
		s.mu.Unlock()
		s.sent <- Sent{Method: "sendDocument", ChatID: chat, MessageID: id, Text: r.FormValue("caption"), File: name}
		reply(w, http.StatusOK, true, telegram.Message{MessageID: id, Chat: telegram.Chat{ID: chat}}, "")
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+Token+"/")
	if !ok {
		reply(w, http.StatusUnauthorized, false, nil, "Unauthorized")
		return
	}
	var p struct {
		Offset          int64           `json:"offset"`
		Timeout         int             `json:"timeout"`
		ChatID          int64           `json:"chat_id"`
		MessageID       int64           `json:"message_id"`
		Text            string          `json:"text"`
		ParseMode       string          `json:"parse_mode"`
		ReplyMarkup     json.RawMessage `json:"reply_markup"`
		CallbackQueryID string          `json:"callback_query_id"`
		FileID          string          `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		reply(w, http.StatusBadRequest, false, nil, "Bad Request: "+err.Error())
		return
	}
	kb, force, placeholder := markupOf(p.ReplyMarkup)
	switch method {
	case "getUpdates":
		s.getUpdates(w, r, p.Offset, p.Timeout)
	case "sendMessage":
		s.mu.Lock()
		id := s.nextMsg
		s.nextMsg++
		s.mu.Unlock()
		s.sent <- Sent{Method: method, ChatID: p.ChatID, MessageID: id, Text: p.Text, ParseMode: p.ParseMode, Keyboard: kb, ForceReply: force, Placeholder: placeholder}
		reply(w, http.StatusOK, true, telegram.Message{MessageID: id, Chat: telegram.Chat{ID: p.ChatID, Type: "private"}, Date: time.Now().Unix(), Text: p.Text}, "")
	case "editMessageText":
		s.sent <- Sent{Method: method, ChatID: p.ChatID, MessageID: p.MessageID, Text: p.Text, ParseMode: p.ParseMode, Keyboard: kb, ForceReply: force, Placeholder: placeholder}
		reply(w, http.StatusOK, true, true, "")
	case "deleteMessage":
		s.sent <- Sent{Method: method, ChatID: p.ChatID, MessageID: p.MessageID}
		reply(w, http.StatusOK, true, true, "")
	case "answerCallbackQuery":
		s.answers <- p.Text
		reply(w, http.StatusOK, true, true, "")
	case "setMyCommands":
		reply(w, http.StatusOK, true, true, "")
	case "getFile":
		s.mu.Lock()
		_, ok := s.files[p.FileID]
		s.mu.Unlock()
		if !ok {
			reply(w, http.StatusBadRequest, false, nil, "file not found")
			return
		}
		reply(w, http.StatusOK, true, telegram.File{FileID: p.FileID, FilePath: "files/" + p.FileID}, "")
	default:
		reply(w, http.StatusNotFound, false, nil, "Not Found: method "+method)
	}
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, offset int64, timeout int) {
	deadline := time.After(time.Duration(timeout) * time.Second)
	for {
		s.mu.Lock()
		var out []telegram.Update
		for _, u := range s.updates {
			if u.UpdateID >= offset {
				out = append(out, u)
			}
		}
		wake := s.wake
		s.mu.Unlock()
		if len(out) > 0 {
			reply(w, http.StatusOK, true, out, "")
			return
		}
		select {
		case <-wake:
		case <-deadline:
			reply(w, http.StatusOK, true, []telegram.Update{}, "")
			return
		case <-r.Context().Done():
			return
		}
	}
}

func markupOf(raw json.RawMessage) (*telegram.InlineKeyboardMarkup, bool, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, ""
	}
	var force struct {
		ForceReply  bool   `json:"force_reply"`
		Placeholder string `json:"input_field_placeholder"`
	}
	if json.Unmarshal(raw, &force) == nil && force.ForceReply {
		return nil, true, force.Placeholder
	}
	var kb telegram.InlineKeyboardMarkup
	if json.Unmarshal(raw, &kb) != nil {
		return nil, false, ""
	}
	return &kb, false, ""
}

func reply(w http.ResponseWriter, status int, ok bool, result any, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"ok": ok}
	if ok {
		body["result"] = result
	} else {
		body["error_code"] = status
		body["description"] = description
	}
	json.NewEncoder(w).Encode(body)
}
