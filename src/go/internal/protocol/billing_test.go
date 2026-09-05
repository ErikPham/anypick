package protocol

import (
	"strings"
	"testing"
)

func TestDropBillingLines(t *testing.T) {
	in := "x-anthropic-billing-header: cc_version=2.1.117; cch=03e12;\nYou are helpful.\n  X-Anthropic-Foo: bar\nMentioning x-anthropic-billing-header mid-line gets masked."
	got := DropBillingLines(in)
	if strings.Contains(got, "cc_version") || strings.Contains(got, "X-Anthropic-Foo") {
		t.Fatalf("full billing lines must go: %q", got)
	}
	if strings.Contains(got, "x-anthropic-billing-header") {
		t.Fatalf("inline marker must be masked: %q", got)
	}
	if !strings.Contains(got, "You are helpful.") {
		t.Fatalf("prose must survive with readable mask: %q", got)
	}
	if !strings.Contains(got, "x-anthropic-billing_header mid-line gets masked.") {
		t.Fatalf("prose must survive with readable mask: %q", got)
	}
}

func TestDropBillingLinesRewritesClaudeCodeIdentity(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude.\nFollow the user."
	got := DropBillingLines(in)
	if strings.Contains(got, "official CLI") || strings.Contains(got, "Claude Code, Anthropic") {
		t.Fatalf("identity fingerprint leaked: %q", got)
	}
	if !strings.Contains(got, "You are a coding assistant.") {
		t.Fatalf("readable paraphrase expected: %q", got)
	}
	sdk := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	gotSDK := DropBillingLines(sdk)
	if strings.Contains(gotSDK, "official CLI") {
		t.Fatalf("SDK superstring leaked: %q", gotSDK)
	}
}

func TestDropBillingLinesRewritesClaudeCodeMainBranchFingerprint(t *testing.T) {
	in := "Current branch: main\n\nMain branch (you will usually use this for PRs): main\n\nGit user: x"
	got := DropBillingLines(in)
	if strings.Contains(got, "usually use this for PRs") {
		t.Fatalf("git-status fingerprint leaked: %q", got)
	}
	if !strings.Contains(got, "Main branch: main") {
		t.Fatalf("readable paraphrase expected: %q", got)
	}
}

func TestDropBillingLinesRemovesBlockedSnippet(t *testing.T) {
	in := "Help text.\n  - To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues\nMore help."
	got := DropBillingLines(in)
	if strings.Contains(got, "github.com/anthropics") {
		t.Fatalf("blocked snippet must be masked: %q", got)
	}
	if !strings.Contains(got, "report the issue at the official tracker") {
		t.Fatalf("readable paraphrase expected: %q", got)
	}
	if !strings.Contains(got, "Help text.") || !strings.Contains(got, "More help.") {
		t.Fatalf("surrounding prose must survive: %q", got)
	}
}

func TestTranslateRequestStripsBillingLine(t *testing.T) {
	in := `{"model":"hy3","messages":[{"role":"user","content":"hi"}],"system":"x-anthropic-billing-header: cc_version=2;\nReal instructions."}`
	out, err := TranslateRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "x-anthropic-billing-header") {
		t.Fatalf("billing line leaked into translated body: %s", out)
	}
	if !strings.Contains(string(out), "Real instructions.") {
		t.Fatalf("system text lost: %s", out)
	}
}

func TestSanitizeOpenAIBody(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"system","content":"x-anthropic-billing-header: v;\nKeep."},{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	out := SanitizeOpenAIBody([]byte(in))
	if strings.Contains(string(out), "x-anthropic-billing-header") {
		t.Fatalf("billing line leaked: %s", out)
	}
	if !strings.Contains(string(out), "Keep.") {
		t.Fatalf("content lost: %s", out)
	}
	if got := string(SanitizeOpenAIBody([]byte(`{oops`))); got != `{oops` {
		t.Fatalf("unparseable body must pass through: %q", got)
	}
}
