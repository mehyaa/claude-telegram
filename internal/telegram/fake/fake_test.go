package fake

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/telegram"
)

func TestUpdatesOffsetAndChat(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	ctx := context.Background()
	when := time.Unix(1_700_000_000, 0)

	fake.SendAt(9, "group", "hi", when)
	ups, err := c.GetUpdates(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].Message.Text != "hi" || ups[0].Message.Chat.ID != 9 || ups[0].Message.Chat.Type != "group" || ups[0].Message.Date != when.Unix() {
		t.Fatalf("GetUpdates = %+v", ups)
	}
	ups, err = c.GetUpdates(ctx, ups[0].UpdateID+1, 0)
	if err != nil || len(ups) != 0 {
		t.Fatalf("GetUpdates after offset = %+v, %v; want none", ups, err)
	}
}

func TestLongPollWakes(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	type result struct {
		ups []telegram.Update
		err error
	}
	done := make(chan result, 1)
	go func() {
		ups, err := c.GetUpdates(context.Background(), 0, 5)
		done <- result{ups, err}
	}()
	time.Sleep(30 * time.Millisecond)
	fake.Send(7, "ping")
	select {
	case r := <-done:
		if r.err != nil || len(r.ups) != 1 || r.ups[0].Message.Text != "ping" {
			t.Fatalf("GetUpdates = %+v, %v", r.ups, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not wake")
	}
}

func TestQuiet(t *testing.T) {
	fake := New(t)
	fake.Quiet(t, 20*time.Millisecond)
}

func TestMessagesEditsAndDeletes(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	ctx := context.Background()
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "A", CallbackData: "a"}},
		{{Text: "B", CallbackData: "b"}},
	}}

	m, err := c.SendMessageMode(ctx, 42, "<b>hi</b>", "HTML", kb)
	if err != nil {
		t.Fatal(err)
	}
	got := fake.Next(t)
	if got.Method != "sendMessage" || got.ChatID != 42 || got.MessageID != m.MessageID || got.Text != "<b>hi</b>" || got.ParseMode != "HTML" {
		t.Fatalf("sent %+v, returned %d", got, m.MessageID)
	}
	if got.Buttons()["A"] != "a" || got.Buttons()["B"] != "b" {
		t.Fatalf("buttons %+v", got.Buttons())
	}

	if _, err := c.SendForceReply(ctx, 42, "ask", "type here"); err != nil {
		t.Fatal(err)
	}
	got = fake.Next(t)
	if !got.ForceReply || got.Placeholder != "type here" || got.Text != "ask" || got.Keyboard != nil {
		t.Fatalf("force reply %+v", got)
	}

	if err := c.EditMessageTextMode(ctx, 42, m.MessageID, "edited", "HTML", nil); err != nil {
		t.Fatal(err)
	}
	got = fake.Next(t)
	if got.Method != "editMessageText" || got.MessageID != m.MessageID || got.Text != "edited" || got.Keyboard != nil {
		t.Fatalf("edit %+v", got)
	}

	if err := c.DeleteMessage(ctx, 42, m.MessageID); err != nil {
		t.Fatal(err)
	}
	got = fake.Next(t)
	if got.Method != "deleteMessage" || got.MessageID != m.MessageID {
		t.Fatalf("delete %+v", got)
	}
}

func TestTapAndToast(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	ctx := context.Background()

	m, err := c.SendMessage(ctx, 42, "choose", nil)
	if err != nil {
		t.Fatal(err)
	}
	fake.Next(t)
	fake.Tap(42, m.MessageID, "yes")
	ups, err := c.GetUpdates(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].CallbackQuery == nil || ups[0].CallbackQuery.Data != "yes" || ups[0].CallbackQuery.Message.MessageID != m.MessageID {
		t.Fatalf("callback %+v", ups)
	}
	if err := c.AnswerCallbackQuery(ctx, ups[0].CallbackQuery.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Answered(t); got != "done" {
		t.Fatalf("toast %q", got)
	}
}

func TestFileDownload(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	ctx := context.Background()

	fake.File(42, "fid", "a.txt", []byte("xyz"), "cap")
	ups, err := c.GetUpdates(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	doc := ups[0].Message.Document
	if len(ups) != 1 || ups[0].Message.Caption != "cap" || doc == nil || doc.FileName != "a.txt" || doc.FileSize != 3 {
		t.Fatalf("file update %+v", ups[0].Message)
	}
	f, err := c.GetFile(ctx, "fid")
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.Download(ctx, f.FilePath, 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "xyz" {
		t.Fatalf("downloaded %q", body)
	}

	if _, err := c.GetFile(ctx, "missing"); err == nil {
		t.Fatal("missing file succeeded")
	}
}

func TestSendDocument(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	if err := c.SendDocument(context.Background(), 42, "n.bin", []byte("abc"), "see"); err != nil {
		t.Fatal(err)
	}
	got := fake.Next(t)
	if got.Method != "sendDocument" || got.ChatID != 42 || got.File != "n.bin" || got.Text != "see" {
		t.Fatalf("document %+v", got)
	}
}

func TestCommandsAndRejectedCalls(t *testing.T) {
	fake := New(t)
	c := telegram.New(fake.URL, Token)
	ctx := context.Background()
	if err := c.SetMyCommands(ctx, []telegram.BotCommand{{Command: "list", Description: "sessions"}}); err != nil {
		t.Fatal(err)
	}

	_, err := telegram.New(fake.URL, "1:nope").GetUpdates(ctx, 0, 0)
	var te *telegram.Error
	if !errors.As(err, &te) || te.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %v", err)
	}

	resp, err := http.Post(fake.URL+"/bot"+Token+"/nope", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("unknown method: %d %s", resp.StatusCode, b)
	}

	resp, err = http.Post(fake.URL+"/bot"+Token+"/sendMessage", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
}
