package runner

import (
	"strings"
	"testing"
)

func TestRedactDropsSecretBearingLines(t *testing.T) {
	input := "request failed\nAuthorization: Bearer should-not-leak\nretry later"
	output := Redact(input)
	if strings.Contains(output, "should-not-leak") || !strings.Contains(output, "<redacted>") {
		t.Fatalf("redaction failed: %q", output)
	}
}
