package domain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	StatusFresh       = "fresh"
	StatusStale       = "stale"
	StatusAuthError   = "auth_error"
	StatusUnavailable = "unavailable"
	StatusUnsupported = "unsupported"
)

type Account struct {
	ID         string            `json:"id"`
	ProviderID string            `json:"providerId"`
	Label      string            `json:"label"`
	Plan       string            `json:"plan,omitempty"`
	Active     bool              `json:"active"`
	Disabled   bool              `json:"disabled,omitempty"`
	Source     string            `json:"source"`
	SourceMeta map[string]string `json:"sourceMeta,omitempty"`
}

type AccountCandidate struct {
	ID         string
	ProviderID string
	Label      string
	Source     string
	SourceMeta map[string]string
	Ref        string
}

type QuotaWindow struct {
	ID                    string     `json:"id"`
	Label                 string     `json:"label"`
	Kind                  string     `json:"kind"`
	Scope                 string     `json:"scope,omitempty"`
	UsedPercent           *float64   `json:"usedPercent,omitempty"`
	RemainingPercent      *float64   `json:"remainingPercent,omitempty"`
	Used                  *float64   `json:"used,omitempty"`
	Limit                 *float64   `json:"limit,omitempty"`
	Remaining             *float64   `json:"remaining,omitempty"`
	Unit                  string     `json:"unit,omitempty"`
	StartsAt              *time.Time `json:"startsAt,omitempty"`
	ResetsAt              *time.Time `json:"resetsAt,omitempty"`
	ExpectedPercent       *float64   `json:"expectedPercent,omitempty"`
	ProjectedExhaustionAt *time.Time `json:"projectedExhaustionAt,omitempty"`
	WillLastToReset       *bool      `json:"willLastToReset,omitempty"`
}

type Snapshot struct {
	AccountID    string        `json:"accountId"`
	FetchedAt    time.Time     `json:"fetchedAt"`
	SourceAgeSec *int64        `json:"sourceAgeSeconds,omitempty"`
	Status       string        `json:"status"`
	Stale        bool          `json:"stale"`
	ErrorCode    string        `json:"errorCode,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	Windows      []QuotaWindow `json:"windows"`
}

type AccountState struct {
	Account  Account  `json:"account"`
	Snapshot Snapshot `json:"snapshot"`
}

type ProviderInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

type Provider interface {
	ID() string
	Name() string
	Discover(ctx context.Context) ([]AccountCandidate, error)
	Fetch(ctx context.Context, account AccountCandidate) (Account, Snapshot, error)
}

type CodedError struct {
	Code string
	Err  error
}

func (e *CodedError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Err.Error()
}

func (e *CodedError) Unwrap() error { return e.Err }

func ErrorCode(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	return "provider_error"
}

func NormalizeWindow(window QuotaWindow) (QuotaWindow, error) {
	window.ID = strings.TrimSpace(window.ID)
	window.Label = strings.TrimSpace(window.Label)
	if window.ID == "" || window.Label == "" {
		return window, errors.New("quota window requires id and label")
	}
	if window.Kind == "" {
		window.Kind = "quota"
	}
	if window.UsedPercent == nil && window.RemainingPercent != nil {
		value := 100 - *window.RemainingPercent
		window.UsedPercent = floatPtr(clampPercent(value))
	}
	if window.RemainingPercent == nil && window.UsedPercent != nil {
		value := 100 - *window.UsedPercent
		window.RemainingPercent = floatPtr(clampPercent(value))
	}
	if window.UsedPercent == nil && window.Used != nil && window.Limit != nil && *window.Limit > 0 {
		value := *window.Used / *window.Limit * 100
		window.UsedPercent = floatPtr(clampPercent(value))
		remaining := 100 - *window.UsedPercent
		window.RemainingPercent = floatPtr(clampPercent(remaining))
	}
	for _, value := range []*float64{window.UsedPercent, window.RemainingPercent, window.Used, window.Limit, window.Remaining} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return window, fmt.Errorf("quota window %q contains a non-finite number", window.ID)
		}
	}
	return window, nil
}

func NormalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	if snapshot.AccountID == "" {
		return snapshot, errors.New("snapshot requires account id")
	}
	if snapshot.FetchedAt.IsZero() {
		snapshot.FetchedAt = time.Now().UTC()
	}
	if snapshot.Status == "" {
		snapshot.Status = StatusFresh
	}
	validStatus := map[string]bool{
		StatusFresh: true, StatusStale: true, StatusAuthError: true,
		StatusUnavailable: true, StatusUnsupported: true,
	}
	if !validStatus[snapshot.Status] {
		return snapshot, fmt.Errorf("unknown snapshot status %q", snapshot.Status)
	}
	seen := make(map[string]struct{}, len(snapshot.Windows))
	normalized := make([]QuotaWindow, 0, len(snapshot.Windows))
	for _, item := range snapshot.Windows {
		window, err := NormalizeWindow(item)
		if err != nil {
			return snapshot, err
		}
		if _, ok := seen[window.ID]; ok {
			return snapshot, fmt.Errorf("duplicate quota window id %q", window.ID)
		}
		seen[window.ID] = struct{}{}
		normalized = append(normalized, window)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		left, right := normalized[i].ResetsAt, normalized[j].ResetsAt
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		return left.Before(*right)
	})
	snapshot.Windows = normalized
	return snapshot, nil
}

func StaleFrom(previous Snapshot, now time.Time, code, message string) Snapshot {
	previous.FetchedAt = now.UTC()
	previous.Status = StatusUnavailable
	previous.Stale = true
	previous.ErrorCode = code
	previous.ErrorMessage = message
	return previous
}

func floatPtr(value float64) *float64 { return &value }

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
