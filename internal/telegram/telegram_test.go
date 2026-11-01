package telegram_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/mehyaa/claude-telegram/internal/telegram"
	"github.com/mehyaa/claude-telegram/internal/telegram/fake"
)

func TestRoundTrip(t *testing.T) {
	api := fake.New(t)
	c := telegram.New(api.URL, fake.Token)
	ctx := context.Background()

	api.Send(42, "hello")
	ups, err := c.GetUpdates(ctx, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].Message.Text != "hello" || ups[0].Message.Chat.ID != 42 {
		t.Fatalf("GetUpdates = %+v", ups)
	}
	// A confirmed offset doesn't return the update again.
	ups, err = c.GetUpdates(ctx, ups[0].UpdateID+1, 1)
	if err != nil || len(ups) != 0 {
		t.Fatalf("GetUpdates after offset = %+v, %v; want none", ups, err)
	}

	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "A", CallbackData: "a"}}}}
	m, err := c.SendMessage(ctx, 42, "hi", kb)
	if err != nil {
		t.Fatal(err)
	}
	got := api.Next(t)
	if got.Text != "hi" || got.MessageID != m.MessageID || got.Buttons()["A"] != "a" {
		t.Fatalf("sent %+v, returned message %d", got, m.MessageID)
	}
	if err := c.EditMessageText(ctx, 42, m.MessageID, "edited", nil); err != nil {
		t.Fatal(err)
	}
	if got := api.Next(t); got.Method != "editMessageText" || got.Text != "edited" || got.Keyboard != nil {
		t.Fatalf("edit = %+v", got)
	}
}

func TestErrorsNeverQuoteTheToken(t *testing.T) {
	// Nothing listens on this port, so the request fails in transport.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	const token = "999:secret-token-value"
	c := telegram.New("http://"+addr, token)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.SendMessage(ctx, 1, "x", nil)
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error quotes the token: %v", err)
	}

	// An API error names the method and Telegram's description only.
	fake := fake.New(t)
	_, err = telegram.New(fake.URL, token).SendMessage(context.Background(), 1, "x", nil)
	var te *telegram.Error
	if !errors.As(err, &te) || te.Code != 401 {
		t.Fatalf("got %v, want a 401 *telegram.Error", err)
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error quotes the token: %v", err)
	}
}

func TestLongTextIsClippedInUTF16Units(t *testing.T) {
	api := fake.New(t)
	c := telegram.New(api.URL, fake.Token)
	// Each 🔔 is two UTF-16 units, so 3000 of them is 6000 units.
	if _, err := c.SendMessage(context.Background(), 1, strings.Repeat("🔔", 3000), nil); err != nil {
		t.Fatal(err)
	}
	got := api.Next(t).Text
	if n := len(utf16.Encode([]rune(got))); n > telegram.MaxText {
		t.Fatalf("sent %d UTF-16 units, limit is %d", n, telegram.MaxText)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("clipped text should end in …")
	}
}
