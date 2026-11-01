package blocks

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want []Block
	}{
		{
			"the taught form, as in this project's own design round",
			"Two things to settle.\n\n" +
				"❓ **Q35** - **Typing into a busy session**: when you send text to a session whose turn is running, should the bot accept the risk?\n" +
				"➡️ Keep typing into busy sessions, as Q5 wants.\n\n" +
				"❓ **Q36** - **Logs**: keep 90 days?\n" +
				"➡️ Yes.\n\nThat's all.",
			[]Block{
				{"Q35", "Typing into a busy session", "when you send text to a session whose turn is running, should the bot accept the risk?", "Keep typing into busy sessions, as Q5 wants."},
				{"Q36", "Logs", "keep 90 days?", "Yes."},
			},
		},
		{
			"plain, several lines, a blank before the recommendation",
			"❓ Q2: Which port should the daemon listen on,\nif any?\n\n➡️ None: a Unix socket\nis enough.\nNext paragraph.",
			[]Block{{"Q2", "", "Which port should the daemon listen on, if any?", "None: a Unix socket is enough. Next paragraph."}},
		},
		{
			"no number, no title, recommendation word",
			"- ❓ Should I squash the commits?\n  ➡️ **Recommendation:** no, keep them small.",
			[]Block{{"", "", "Should I squash the commits?", "no, keep them small."}},
		},
		{
			"no recommendation, and a block ended by a blank line",
			"❓ **Q7** - **Naming**: ctg or cctg?\n\nUnrelated text.",
			[]Block{{"Q7", "Naming", "ctg or cctg?", ""}},
		},
		{
			"code blocks don't count",
			"```\n❓ Q1: not a question\n```\n❓ Q2: a question?",
			[]Block{{"Q2", "", "a question?", ""}},
		},
		{"no blocks", "Just a reply.\nWith ➡️ an arrow.", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.text); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}
