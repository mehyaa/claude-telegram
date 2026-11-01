// Command post is the step-1 probe (throwaway): post one message to a
// session's messaging socket.
//
//	go run ./probe/post <socket> <text>
//
// With PROBE_AUTH=1, sends an auth line first, taking the token from
// CLAUDE_CODE_MESSAGING_TOKEN. Prints whatever the socket sends back within
// 3s. Never prints the token.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

const replyWait = 3 * time.Second

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: post <socket> <text>\n")
		os.Exit(2)
	}
	lines, kind, err := outgoing(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn, err := net.DialTimeout("unix", os.Args[1], replyWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := writeLines(conn, lines); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("sent %d lines; %s\n", len(lines), kind)
	reply, timedOut, err := readReply(conn, replyWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if timedOut {
		fmt.Println("reply timed out after 3 s")
	}
	fmt.Printf("reply: %s\n", show(reply))
}

// outgoing is the JSON lines to write. kind is "auth" or "no auth", matching
// the status line the probe prints.
func outgoing(text string) ([][]byte, string, error) {
	var lines [][]byte
	kind := "no auth"
	if os.Getenv("PROBE_AUTH") == "1" {
		token, ok := os.LookupEnv("CLAUDE_CODE_MESSAGING_TOKEN")
		if !ok {
			return nil, "", errors.New("PROBE_AUTH=1 and CLAUDE_CODE_MESSAGING_TOKEN is unset")
		}
		b, err := marshal(authLine{Type: "auth", Token: token})
		if err != nil {
			return nil, "", err
		}
		lines = append(lines, b)
		kind = "auth"
	}
	b, err := marshal(userLine{
		Type:    "user",
		Message: userBody{Role: "user", Content: text},
	})
	if err != nil {
		return nil, "", err
	}
	return append(lines, b), kind, nil
}

type authLine struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type userLine struct {
	Type    string   `json:"type"`
	Message userBody `json:"message"`
}

type userBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeLines writes each line, then half-closes the socket so the session
// sees the end of the request and can reply.
func writeLines(conn net.Conn, lines [][]byte) error {
	if err := conn.SetDeadline(time.Now().Add(replyWait)); err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := conn.Write(line); err != nil {
			return err
		}
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	return uc.CloseWrite()
}

// readReply reads until the peer closes or a read waits longer than wait.
func readReply(conn net.Conn, wait time.Duration) ([]byte, bool, error) {
	var reply []byte
	buf := make([]byte, 4096)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
			return reply, false, err
		}
		n, err := conn.Read(buf)
		reply = append(reply, buf[:n]...)
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return reply, false, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return reply, true, nil
		}
		return reply, false, err
	}
}

func show(b []byte) string {
	return strconv.Quote(string(bytes.ToValidUTF8(b, []byte("\uFFFD"))))
}
