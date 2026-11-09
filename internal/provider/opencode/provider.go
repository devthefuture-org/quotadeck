package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
)

type Provider struct {
	config config.OpenCodeConfig
	client *http.Client
}

func New(cfg config.OpenCodeConfig) *Provider { return &Provider{config: cfg, client: secureClient()} }
func (*Provider) ID() string                  { return "opencode" }
func (*Provider) Name() string                { return "OpenCode Go" }
func (p *Provider) Discover(ctx context.Context) ([]domain.AccountCandidate, error) {
	credentials, err := LocalCredentials(ctx, p.config)
	if err != nil {
		return nil, err
	}
	result := make([]domain.AccountCandidate, 0, len(credentials))
	for _, c := range credentials {
		result = append(result, domain.AccountCandidate{ID: c.AccountID(), ProviderID: p.ID(), Label: c.Label, Source: "opencode-v2", Ref: c.ID, SourceMeta: map[string]string{"path": c.Database, "secretPresent": "true"}})
	}
	return result, nil
}
func (p *Provider) Fetch(ctx context.Context, candidate domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	account := domain.Account{ID: candidate.ID, ProviderID: p.ID(), Label: candidate.Label, Plan: "Go", Source: candidate.Source, SourceMeta: candidate.SourceMeta}
	snapshot := domain.Snapshot{AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh}
	credentials, err := LocalCredentials(ctx, p.config)
	if err != nil {
		return account, snapshot, err
	}
	var credential *Credential
	for _, c := range credentials {
		if c.ID == candidate.Ref {
			credential = &c
			break
		}
	}
	if credential == nil {
		return account, snapshot, &domain.CodedError{Code: "opencode_auth_error", Err: ErrLoginRequired}
	}
	account.Active = credential.Active
	credential.client = p.client
	token, err := credential.Token(ctx)
	if err != nil {
		return account, snapshot, &domain.CodedError{Code: "opencode_auth_error", Err: err}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://opencode.ai/inference/go/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-opencode-org-id", credential.OrgID)
	req.Header.Set("User-Agent", "QuotaDeck")
	req.Header.Set("Accept", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return account, snapshot, &domain.CodedError{Code: "opencode_unavailable", Err: errors.New("OpenCode Go quota service is unavailable")}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		code := "opencode_unavailable"
		if response.StatusCode == 401 || response.StatusCode == 403 {
			code = "opencode_auth_error"
		}
		return account, snapshot, &domain.CodedError{Code: code, Err: fmt.Errorf("OpenCode Go quotas returned HTTP %d; verify your Console login and Go subscription", response.StatusCode)}
	}
	snapshot.Windows, err = parseUsage(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return account, snapshot, &domain.CodedError{Code: "opencode_invalid_response", Err: err}
	}
	return account, snapshot, nil
}
func parseUsage(reader io.Reader) ([]domain.QuotaWindow, error) {
	var payload struct {
		Usage map[string]struct {
			Status   string    `json:"status"`
			Percent  *float64  `json:"percent"`
			ResetsAt time.Time `json:"resetsAt"`
		} `json:"usage"`
	}
	if json.NewDecoder(reader).Decode(&payload) != nil {
		return nil, errors.New("invalid OpenCode Go quota response")
	}
	result := make([]domain.QuotaWindow, 0, 3)
	for _, item := range []struct{ id, label string }{{"rolling", "5 hours"}, {"weekly", "Weekly"}, {"monthly", "Monthly"}} {
		meter, ok := payload.Usage[item.id]
		if !ok || meter.Percent == nil || *meter.Percent < 0 || *meter.Percent > 100 || meter.ResetsAt.IsZero() || (meter.Status != "ok" && meter.Status != "rate-limited") {
			return nil, errors.New("incomplete OpenCode Go quota response")
		}
		result = append(result, domain.QuotaWindow{ID: item.id, Label: item.label, Kind: "quota", UsedPercent: meter.Percent, ResetsAt: &meter.ResetsAt})
	}
	return result, nil
}
