package codex

import (
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func TestParseMultiBucketRateLimits(t *testing.T) {
	accountJSON := []byte(`{"account":{"type":"chatgpt","email":"redacted@example.invalid","planType":"plus"},"requiresOpenaiAuth":false}`)
	limitsJSON := []byte(`{
      "rateLimits": {"planType":"plus"},
      "rateLimitsByLimitId": {
        "codex": {
          "limitId":"codex",
          "limitName":"Codex",
          "planType":"plus",
          "primary":{"usedPercent":31,"resetsAt":1900000000,"windowDurationMins":300},
          "secondary":{"usedPercent":52,"resetsAt":1900100000,"windowDurationMins":10080}
        },
        "code-review": {
          "limitName":"Code review",
          "primary":{"usedPercent":8,"windowDurationMins":10080}
        }
      }
    }`)
	candidate := domain.AccountCandidate{ID: "codex:home:test", ProviderID: "codex", Label: "Personal", Source: "CODEX_HOME"}
	account, snapshot, err := Parse(accountJSON, limitsJSON, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if account.Plan != "plus" || len(snapshot.Windows) != 3 {
		t.Fatalf("unexpected Codex parse: account=%#v snapshot=%#v", account, snapshot)
	}
}

func TestParseRequiresAuthentication(t *testing.T) {
	candidate := domain.AccountCandidate{ID: "codex:home:test", ProviderID: "codex", Label: "Work", Source: "CODEX_HOME"}
	_, _, err := Parse([]byte(`{"account":null,"requiresOpenaiAuth":true}`), []byte(`{}`), candidate)
	if err == nil || domain.ErrorCode(err) != "codex_auth_required" {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestParseAuthenticatedAccountThatRequiresOpenAIAuth(t *testing.T) {
	accountJSON := []byte(`{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}`)
	limitsJSON := []byte(`{"rateLimits":{"planType":"plus","primary":{"usedPercent":12,"windowDurationMins":300}}}`)
	candidate := domain.AccountCandidate{ID: "codex:home:test", ProviderID: "codex", Label: "Personal", Source: "CODEX_HOME"}

	account, snapshot, err := Parse(accountJSON, limitsJSON, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if account.Plan != "plus" || snapshot.Status != domain.StatusFresh || len(snapshot.Windows) != 1 {
		t.Fatalf("unexpected authenticated Codex state: account=%#v snapshot=%#v", account, snapshot)
	}
}
