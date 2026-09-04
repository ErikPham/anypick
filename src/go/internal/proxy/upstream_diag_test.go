package proxy

import (
	"strings"
	"testing"
)

// The 11128 fingerprint lands in proxy.log, and proxy.log gets attached to
// bug reports. Naming the model and counting the messages is useful;
// repeating what the user typed is not.
func TestDescribeSanitizedOpenAILeaksNoPromptText(t *testing.T) {
	body := []byte(`{"model":"hy4-preview","messages":[{"role":"user","content":"my-secret-prompt"}],"tools":[{"name":"t"}]}`)
	got := describeSanitizedOpenAI(body)
	for _, want := range []string{"model=hy4-preview", "n=1", "lead=user", "tools=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fingerprint = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "my-secret-prompt") {
		t.Fatalf("fingerprint leaked prompt text: %q", got)
	}
}

// A body that is not a JSON object must not panic or spill raw bytes.
func TestDescribeSanitizedOpenAIUnparseable(t *testing.T) {
	if got := describeSanitizedOpenAI([]byte("not json at all")); got != "unparseable" {
		t.Fatalf("fingerprint = %q, want \"unparseable\"", got)
	}
}
