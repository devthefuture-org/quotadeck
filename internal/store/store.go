package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(0)
	result := &Store{db: db}
	if err := result.configure(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := result.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return result, nil
}

func sqliteDSN(path string) string {
	var parsed *url.URL
	if path == ":memory:" {
		parsed = &url.URL{Scheme: "file", Opaque: ":memory:"}
	} else {
		parsed = &url.URL{Scheme: "file", Path: path}
	}
	query := parsed.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(NORMAL)")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
            version INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS accounts (
            id TEXT PRIMARY KEY,
            provider_id TEXT NOT NULL,
            label TEXT NOT NULL,
            plan TEXT NOT NULL DEFAULT '',
            active INTEGER NOT NULL DEFAULT 0,
            disabled INTEGER NOT NULL DEFAULT 0,
            source TEXT NOT NULL,
            source_meta_json TEXT NOT NULL DEFAULT '{}',
            updated_at TEXT NOT NULL
        )`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_provider ON accounts(provider_id, label)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
            fetched_at TEXT NOT NULL,
            source_age_seconds INTEGER,
            status TEXT NOT NULL,
            stale INTEGER NOT NULL DEFAULT 0,
            error_code TEXT NOT NULL DEFAULT '',
            error_message TEXT NOT NULL DEFAULT ''
        )`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_account_time ON snapshots(account_id, fetched_at DESC)`,
		`CREATE TABLE IF NOT EXISTS quota_windows (
            snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
            position INTEGER NOT NULL,
            window_id TEXT NOT NULL,
            payload_json TEXT NOT NULL,
            PRIMARY KEY(snapshot_id, window_id)
        )`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, datetime('now'))`,
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer transaction.Rollback()
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (s *Store) Save(ctx context.Context, account domain.Account, snapshot domain.Snapshot) error {
	normalized, err := domain.NormalizeSnapshot(snapshot)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(account.SourceMeta)
	if err != nil {
		return fmt.Errorf("encode source metadata: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot transaction: %w", err)
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `
        INSERT INTO accounts(id, provider_id, label, plan, active, disabled, source, source_meta_json, updated_at)
        VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            provider_id=excluded.provider_id,
            label=excluded.label,
            plan=excluded.plan,
            active=excluded.active,
            disabled=excluded.disabled,
            source=excluded.source,
            source_meta_json=excluded.source_meta_json,
            updated_at=excluded.updated_at`,
		account.ID, account.ProviderID, account.Label, account.Plan, account.Active, account.Disabled,
		account.Source, string(meta), normalized.FetchedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert account: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `
        INSERT INTO snapshots(account_id, fetched_at, source_age_seconds, status, stale, error_code, error_message)
        VALUES(?, ?, ?, ?, ?, ?, ?)`,
		normalized.AccountID, normalized.FetchedAt.UTC().Format(time.RFC3339Nano), normalized.SourceAgeSec,
		normalized.Status, normalized.Stale, normalized.ErrorCode, normalized.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	snapshotID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read snapshot id: %w", err)
	}
	for index, window := range normalized.Windows {
		payload, err := json.Marshal(window)
		if err != nil {
			return fmt.Errorf("encode quota window: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO quota_windows(snapshot_id, position, window_id, payload_json)
            VALUES(?, ?, ?, ?)`, snapshotID, index, window.ID, string(payload)); err != nil {
			return fmt.Errorf("insert quota window: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	return nil
}

func (s *Store) LatestStates(ctx context.Context) ([]domain.AccountState, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT a.id, a.provider_id, a.label, a.plan, a.active, a.disabled, a.source, a.source_meta_json,
               s.id, s.fetched_at, s.source_age_seconds, s.status, s.stale, s.error_code, s.error_message
        FROM accounts a
        JOIN snapshots s ON s.id = (
            SELECT s2.id FROM snapshots s2 WHERE s2.account_id = a.id ORDER BY s2.fetched_at DESC, s2.id DESC LIMIT 1
        )
        ORDER BY a.provider_id, a.label`)
	if err != nil {
		return nil, fmt.Errorf("query current state: %w", err)
	}
	defer rows.Close()
	var states []domain.AccountState
	for rows.Next() {
		state, snapshotID, err := scanState(rows)
		if err != nil {
			return nil, err
		}
		windows, err := s.windows(ctx, snapshotID)
		if err != nil {
			return nil, err
		}
		state.Snapshot.Windows = windows
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current state: %w", err)
	}
	return states, nil
}

func (s *Store) Latest(ctx context.Context, accountID string) (domain.AccountState, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT a.id, a.provider_id, a.label, a.plan, a.active, a.disabled, a.source, a.source_meta_json,
               s.id, s.fetched_at, s.source_age_seconds, s.status, s.stale, s.error_code, s.error_message
        FROM accounts a
        JOIN snapshots s ON s.account_id = a.id
        WHERE a.id = ?
        ORDER BY s.fetched_at DESC, s.id DESC LIMIT 1`, accountID)
	state, snapshotID, err := scanState(row)
	if err != nil {
		return domain.AccountState{}, err
	}
	state.Snapshot.Windows, err = s.windows(ctx, snapshotID)
	return state, err
}

func (s *Store) AccountsByProvider(ctx context.Context, providerID string) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, provider_id, label, plan, active, disabled, source, source_meta_json
        FROM accounts WHERE provider_id = ? ORDER BY label`, providerID)
	if err != nil {
		return nil, fmt.Errorf("query provider accounts: %w", err)
	}
	defer rows.Close()
	var accounts []domain.Account
	for rows.Next() {
		var account domain.Account
		var active, disabled bool
		var meta string
		if err := rows.Scan(&account.ID, &account.ProviderID, &account.Label, &account.Plan, &active, &disabled, &account.Source, &meta); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		account.Active, account.Disabled = active, disabled
		_ = json.Unmarshal([]byte(meta), &account.SourceMeta)
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *Store) History(ctx context.Context, accountID string, from, to time.Time) ([]domain.Snapshot, error) {
	query := `SELECT id, fetched_at, source_age_seconds, status, stale, error_code, error_message
              FROM snapshots WHERE account_id = ?`
	args := []any{accountID}
	if !from.IsZero() {
		query += " AND fetched_at >= ?"
		args = append(args, from.UTC().Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		query += " AND fetched_at <= ?"
		args = append(args, to.UTC().Format(time.RFC3339Nano))
	}
	query += " ORDER BY fetched_at DESC LIMIT 2000"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	var history []domain.Snapshot
	for rows.Next() {
		var snapshotID int64
		var fetchedAt string
		var sourceAge sql.NullInt64
		var snapshot domain.Snapshot
		if err := rows.Scan(&snapshotID, &fetchedAt, &sourceAge, &snapshot.Status, &snapshot.Stale, &snapshot.ErrorCode, &snapshot.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		snapshot.AccountID = accountID
		snapshot.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
		if sourceAge.Valid {
			value := sourceAge.Int64
			snapshot.SourceAgeSec = &value
		}
		snapshot.Windows, err = s.windows(ctx, snapshotID)
		if err != nil {
			return nil, err
		}
		history = append(history, snapshot)
	}
	return history, rows.Err()
}

func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE fetched_at < ?", before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune snapshots: %w", err)
	}
	return result.RowsAffected()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanState(row scanner) (domain.AccountState, int64, error) {
	var state domain.AccountState
	var active, disabled bool
	var sourceMeta, fetchedAt string
	var sourceAge sql.NullInt64
	var snapshotID int64
	err := row.Scan(
		&state.Account.ID, &state.Account.ProviderID, &state.Account.Label, &state.Account.Plan,
		&active, &disabled, &state.Account.Source, &sourceMeta,
		&snapshotID, &fetchedAt, &sourceAge, &state.Snapshot.Status, &state.Snapshot.Stale,
		&state.Snapshot.ErrorCode, &state.Snapshot.ErrorMessage,
	)
	if err != nil {
		return state, 0, err
	}
	state.Account.Active, state.Account.Disabled = active, disabled
	_ = json.Unmarshal([]byte(sourceMeta), &state.Account.SourceMeta)
	state.Snapshot.AccountID = state.Account.ID
	state.Snapshot.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
	if sourceAge.Valid {
		value := sourceAge.Int64
		state.Snapshot.SourceAgeSec = &value
	}
	return state, snapshotID, nil
}

func (s *Store) windows(ctx context.Context, snapshotID int64) ([]domain.QuotaWindow, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT payload_json FROM quota_windows WHERE snapshot_id = ? ORDER BY position`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query quota windows: %w", err)
	}
	defer rows.Close()
	var windows []domain.QuotaWindow
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan quota window: %w", err)
		}
		var window domain.QuotaWindow
		if err := json.Unmarshal([]byte(payload), &window); err != nil {
			return nil, fmt.Errorf("decode quota window: %w", err)
		}
		windows = append(windows, window)
	}
	return windows, rows.Err()
}
