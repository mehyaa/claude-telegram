package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/mehyaa/claude-telegram/internal/transcript"
)

func TestHTMLBodyKeepsCodeBlocks(t *testing.T) {
	got := htmlBody("See:\n```go\nif a < b {\n}\n```\n& done")
	want := "See:\n<pre><code class=\"language-go\">if a &lt; b {\n}</code></pre>\n&amp; done"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestPeerAndSendMessageAttribution(t *testing.T) {
	d := &Daemon{
		cfg: Config{Now: time.Now},
		st: state{
			Sessions: []*session{
				{ID: "s1", Num: 1, PID: 101, Name: "main"},
				{ID: "s3", Num: 3, PID: 303, Name: "api-work"},
			},
		},
	}
	s1 := d.st.Sessions[0]

	head, body := d.entryHTML(s1, transcript.Entry{Kind: transcript.Peer, PeerPID: 303, Text: "done"})
	if head != "📨 <b>#1 main</b> · From session 3 (api-work)" || body != "done" {
		t.Fatalf("peer known: head=%q body=%q", head, body)
	}
	head, _ = d.entryHTML(s1, transcript.Entry{Kind: transcript.Peer, PeerPID: 999, Text: "done"})
	if head != "📨 <b>#1 main</b> · From another session" {
		t.Fatalf("peer unknown: head=%q", head)
	}

	_, tool := d.entryHTML(s1, transcript.Entry{
		Kind:     transcript.Tool,
		ToolName: "SendMessage",
		Desc:     "api-work",
		Text:     "please rebase",
	})
	if want := "📤 <b>#1 main</b> · To session 3 (api-work)\nplease rebase"; tool != want {
		t.Fatalf("SendMessage known: got %q, want %q", tool, want)
	}
}

func TestSingleLineHeaderAndTerminalEntry(t *testing.T) {
	now := time.Date(2026, 10, 31, 14, 30, 0, 0, time.Local)
	d := &Daemon{
		cfg: Config{Now: func() time.Time { return now }},
	}
	s := &session{ID: "s2", Num: 2, Name: "claude-telegram"}
	d.notePhone("s2", "from phone")

	head, body := d.entryHTMLAt(s, transcript.Entry{
		Kind: transcript.User,
		Text: "from phone",
		Time: now,
	}, 1, 3)
	if head != "👤 <b>#2 claude-telegram</b> · You (Telegram) · 1/3 · 14:30" || body != "from phone" {
		t.Fatalf("telegram prompt: head=%q body=%q", head, body)
	}

	head, body = d.entryHTMLAt(s, transcript.Entry{
		Kind:   transcript.User,
		Term:   true,
		Text:   "git status -s",
		Result: "M internal/daemon/format.go",
		Time:   now,
	}, 2, 3)
	if head != "🖥 <b>#2 claude-telegram</b> · Terminal · 2/3 · 14:30" {
		t.Fatalf("terminal head=%q", head)
	}
	if got := htmlBody(body); got != "<pre><code class=\"language-bash\">$ git status -s</code></pre>\n<pre>M internal/daemon/format.go</pre>" {
		t.Fatalf("terminal body=%q", got)
	}

	head, body = d.entryHTMLAt(s, transcript.Entry{
		Kind: transcript.Assistant,
		Text: "All clean.",
		Time: now,
	}, 3, 3)
	if head != "🤖 <b>#2 claude-telegram</b> · Claude · 3/3 · 14:30" || body != "All clean." {
		t.Fatalf("assistant: head=%q body=%q", head, body)
	}
}

func TestToolHTMLMasksSecrets(t *testing.T) {
	now := time.Now()
	d := &Daemon{cfg: Config{Now: func() time.Time { return now }}}
	s := &session{
		ID:      "s1",
		secrets: []secret{{text: "hunter2", until: now.Add(time.Minute)}},
	}
	_, tool := d.entryHTML(s, transcript.Entry{
		Kind:     transcript.Tool,
		ToolName: "Bash",
		Desc:     "login with hunter2",
		Command:  "echo hunter2",
		Output:   "pw=hunter2",
		Status:   transcript.OK,
	})
	if strings.Contains(tool, "hunter2") || !strings.Contains(tool, "••••") {
		t.Fatalf("secret not masked in toolHTML: %q", tool)
	}
}

func TestNoteCmdAndInterruptedFormatting(t *testing.T) {
	d := &Daemon{cfg: Config{Now: time.Now}}
	head, _ := d.entryHTML(nil, transcript.Entry{Kind: transcript.Note, Cmd: true, Text: "task finished"})
	if head != "⚙️ task finished" {
		t.Fatalf("cmd note: got %q", head)
	}
	head, _ = d.entryHTML(nil, transcript.Entry{Kind: transcript.Note, Text: "interrupted"})
	if head != "⏹ interrupted" {
		t.Fatalf("interrupted note: got %q", head)
	}
}

func TestMarkdownToHTMLConversion(t *testing.T) {
	src := strings.Join([]string{
		"### Summary Heading",
		"Here is **bold**, *italic*, _also italic_, ~~strike~~, and `inline_code<T>`.",
		"Keep identifiers like foo_bar_baz and paths like /home/user/my_file_name.go intact.",
		"See [docs](https://example.com/a?x=1&y=2).",
		"> quoted line 1",
		"> quoted line 2",
		"- first item",
		"* second item",
		"---",
		"| Name | Value |",
		"| --- | --- |",
		"| alpha | **10** |",
		"| beta_key | `20` |",
	}, "\n")

	got := htmlBody(src)
	for _, want := range []string{
		"<b>Summary Heading</b>",
		"<b>bold</b>",
		"<i>italic</i>",
		"<i>also italic</i>",
		"<s>strike</s>",
		"<code>inline_code&lt;T&gt;</code>",
		"foo_bar_baz",
		"/home/user/my_file_name.go",
		`<a href="https://example.com/a?x=1&amp;y=2">docs</a>`,
		"<blockquote>quoted line 1\nquoted line 2</blockquote>",
		"• first item",
		"• second item",
		"───",
		"<pre>Name     | Value\n---------+------\nalpha    | 10\nbeta_key | 20</pre>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("htmlBody missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatBashCommandAndPreBlock(t *testing.T) {
	cmd := `cd /work && git status -s; echo "a && b; c" || exit 1`
	got := formatBashCommand(cmd)
	want := "cd /work &&\ngit status -s;\necho \"a && b; c\" ||\nexit 1"
	if got != want {
		t.Fatalf("formatBashCommand:\ngot:\n%s\nwant:\n%s", got, want)
	}

	// Heredocs must not be split on && or ; inside their body.
	heredoc := "cat <<'EOF' > out.txt\na && b; c\nEOF\necho done"
	if gotHeredoc := formatBashCommand(heredoc); gotHeredoc != heredoc {
		t.Fatalf("heredoc should remain intact:\ngot:\n%s\nwant:\n%s", gotHeredoc, heredoc)
	}

	d := &Daemon{cfg: Config{Now: time.Now}}
	s := &session{ID: "s1", Num: 1, Name: "app"}
	_, card := d.entryHTML(s, transcript.Entry{
		Kind:     transcript.Tool,
		ToolName: "Bash",
		Desc:     "Check status and edits",
		Path:     "internal/foo.go",
		Plus:     1,
		Minus:    1,
		Command:  cmd,
		Args:     []transcript.ToolArg{{Name: "Timeout", Value: "30s"}},
		Diff:     "--- a/internal/foo.go\n+++ b/internal/foo.go\n@@ -1 +1 @@\n-old\n+new",
		Output:   "ok",
		Status:   transcript.OK,
	})
	for _, piece := range []string{
		"<b>Path:</b> <code>internal/foo.go</code>",
		"<b>Change:</b> +1 −1",
		"<b>Command:</b>\n<pre><code class=\"language-bash\">",
		"<b>Timeout:</b> <code>30s</code>",
		"<pre><code class=\"language-diff\">",
		"<b>Result:</b> ✓",
		"<blockquote expandable>ok</blockquote>",
	} {
		if !strings.Contains(card, piece) {
			t.Fatalf("Bash tool card missing %q in:\n%s", piece, card)
		}
	}
}

func TestSubagentPeerAttributionAndEditDiffSuppression(t *testing.T) {
	d := &Daemon{cfg: Config{Now: time.Now}}
	s := &session{ID: "s1", Num: 1, Name: "main"}

	head, body := d.entryHTML(s, transcript.Entry{
		Kind:     transcript.Peer,
		Subagent: true,
		PeerFrom: "a3123085",
		PeerName: "general-purpose",
		Text:     "Here is **the** report.",
	})
	if head != "📨 <b>#1 main</b> · Subagent general-purpose (a3123085)" {
		t.Fatalf("subagent head = %q", head)
	}
	if got := htmlBody(body); got != "Here is <b>the</b> report." {
		t.Fatalf("subagent body = %q", got)
	}

	// Edit tool card in Full mode shows inline diff and suppresses redundant "updated successfully" output.
	_, editCard := d.entryHTML(s, transcript.Entry{
		Kind:     transcript.Tool,
		ToolName: "Edit",
		Path:     "main.go",
		Plus:     1,
		Minus:    1,
		Diff:     "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new",
		Output:   "The file main.go has been updated successfully.",
		Status:   transcript.OK,
	})
	if !strings.Contains(editCard, "<pre><code class=\"language-diff\">") {
		t.Fatalf("Edit card missing inline diff:\n%s", editCard)
	}
	if strings.Contains(editCard, "updated successfully") {
		t.Fatalf("Edit card should suppress redundant success text when diff is shown:\n%s", editCard)
	}
}
