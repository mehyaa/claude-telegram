// Command faketg is the step-3 live probe's phone (throwaway): the fake Bot
// API, with control endpoints to type and tap as the owner.
//
//	faketg <listen-addr> <log>
//
// Every message and edit the bot sends, and every toast, is appended to
// <log> as a JSON line. Control:
//
//	curl -d 'text' 'http://ADDR/ctl/send?chat=42'
//	curl -X POST 'http://ADDR/ctl/tap?chat=42&msg=1003&data=pa:1.abcd:1.1'
//
// The Bot API root is http://ADDR/api (set CTG_TELEGRAM_API to it).
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/mehyaa/claude-telegram/internal/telegram/telegramtest"
)

func main() {
	addr, logPath := os.Args[1], os.Args[2]
	fake := telegramtest.Start()
	defer fake.Close()
	out, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	write := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
	}
	go func() {
		for {
			select {
			case m := <-fake.Sent():
				write(map[string]any{"t": time.Now().Format("15:04:05.000"), "method": m.Method, "msg": m.MessageID, "text": m.Text, "buttons": m.Buttons()})
			case a := <-fake.Answers():
				write(map[string]any{"t": time.Now().Format("15:04:05.000"), "toast": a})
			}
		}
	}()

	target, _ := url.Parse(fake.URL)
	api := httputil.NewSingleHostReverseProxy(target)
	http.Handle("/api/", http.StripPrefix("/api", api))
	http.HandleFunc("/ctl/send", func(w http.ResponseWriter, r *http.Request) {
		chat, _ := strconv.ParseInt(r.URL.Query().Get("chat"), 10, 64)
		text, _ := io.ReadAll(r.Body)
		fake.Send(chat, string(text))
		write(map[string]any{"t": time.Now().Format("15:04:05.000"), "phone_sent": string(text)})
	})
	http.HandleFunc("/ctl/tap", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		chat, _ := strconv.ParseInt(q.Get("chat"), 10, 64)
		msg, _ := strconv.ParseInt(q.Get("msg"), 10, 64)
		fake.Tap(chat, msg, q.Get("data"))
		write(map[string]any{"t": time.Now().Format("15:04:05.000"), "phone_tapped": q.Get("data"), "on": msg})
	})
	log.Fatal(http.ListenAndServe(addr, nil))
}
