package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutgoingAuth(t *testing.T) {
	t.Setenv("PROBE_AUTH", "1")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "secret-token")
	lines, kind, err := outgoing("hello <world>")
	if err != nil {
		t.Fatal(err)
	}
	if kind != "auth" || len(lines) != 2 {
		t.Fatalf("kind %q lines %d", kind, len(lines))
	}
	var auth authLine
	if err := json.Unmarshal(lines[0], &auth); err != nil {
		t.Fatal(err)
	}
	if auth.Type != "auth" || auth.Token != "secret-token" {
		t.Fatalf("auth line: %+v", auth)
	}
	var user userLine
	if err := json.Unmarshal(lines[1], &user); err != nil {
		t.Fatal(err)
	}
	if user.Message.Content != "hello <world>" {
		t.Fatalf("content escaped or dropped: %+v", user)
	}
	if bytes.Contains(lines[1], []byte(`\u003c`)) {
		t.Fatalf("html escaped: %s", lines[1])
	}
}

func TestOutgoingNoAuth(t *testing.T) {
	t.Setenv("PROBE_AUTH", "")
	lines, kind, err := outgoing("hi")
	if err != nil {
		t.Fatal(err)
	}
	if kind != "no auth" || len(lines) != 1 {
		t.Fatalf("kind %q lines %d", kind, len(lines))
	}
	if bytes.Contains(lines[0], []byte("token")) {
		t.Fatalf("token field on the user line: %s", lines[0])
	}
}

func TestOutgoingAuthRequiresToken(t *testing.T) {
	t.Setenv("PROBE_AUTH", "1")
	val, ok := os.LookupEnv("CLAUDE_CODE_MESSAGING_TOKEN")
	os.Unsetenv("CLAUDE_CODE_MESSAGING_TOKEN")
	t.Cleanup(func() {
		if ok {
			os.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", val)
		}
	})
	if _, _, err := outgoing("hi"); err == nil {
		t.Fatal("expected an error when the token is unset")
	}
}

func TestExchange(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b, err := io.ReadAll(c)
		if err != nil {
			errc <- err
			return
		}
		got <- b
		_, err = io.WriteString(c, "pong\n")
		errc <- err
	}()

	conn, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	lines := [][]byte{[]byte("{\"type\":\"user\"}\n")}
	if err := writeLines(conn, lines); err != nil {
		t.Fatal(err)
	}
	reply, timedOut, err := readReply(conn, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut {
		t.Fatal("timed out")
	}
	if string(reply) != "pong\n" {
		t.Fatalf("reply %q", reply)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish")
	}
	select {
	case b := <-got:
		if string(b) != string(lines[0]) {
			t.Fatalf("server got %q", b)
		}
	default:
		t.Fatal("server got nothing")
	}
}

func TestReadReplyTimesOut(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		time.Sleep(time.Second)
	}()
	conn, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "hi\n"); err != nil {
		t.Fatal(err)
	}
	reply, timedOut, err := readReply(conn, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut {
		t.Fatal("expected a timeout")
	}
	if len(reply) != 0 {
		t.Fatalf("reply %q", reply)
	}
}

func TestShowKeepsReplyOnOneLine(t *testing.T) {
	got := show([]byte("ok\n"))
	if strings.Contains(got, "\n") {
		t.Fatalf("quote broke the line: %s", got)
	}
	if got != `"ok\n"` {
		t.Fatalf("quote: %s", got)
	}
}
