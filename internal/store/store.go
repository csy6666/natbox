package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type ManagedContainer struct {
	Name            string `json:"name"`
	Image           string `json:"image"`
	MemoryLimitMb   int64  `json:"memoryLimitMb"`
	DiskLimitGb     int64  `json:"diskLimitGb"`
	CPULimit        string `json:"cpuLimit"`
	DesiredState    string `json:"desiredState"`
	QuotaBytes      int64  `json:"quotaBytes"`
	QuotaRxBaseline int64  `json:"quotaRxBaseline"`
	QuotaTxBaseline int64  `json:"quotaTxBaseline"`
	QuotaResetAt    string `json:"quotaResetAt,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type AuditEvent struct {
	ID        int64  `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	TargetID  string `json:"targetId"`
	Result    string `json:"result"`
	SourceIP  string `json:"sourceIp,omitempty"`
	Details   string `json:"details,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type Backup struct {
	Version     int                `json:"version"`
	GeneratedAt string             `json:"generatedAt"`
	Containers  []ManagedContainer `json:"containers"`
}

var managedNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		_ = db.Close()
		return nil, fmt.Errorf("protect database file: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) BackupDatabase(ctx context.Context, destination string) error {
	if strings.TrimSpace(destination) == "" || !filepath.IsAbs(destination) {
		return errors.New("backup destination must be an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	temporary := destination + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_ = os.Remove(temporary)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, temporary); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("vacuum database backup: %w", err)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("protect database backup: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish database backup: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_containers (
			name TEXT PRIMARY KEY,
			image TEXT NOT NULL,
			memory_limit_mb INTEGER NOT NULL,
			disk_limit_gb INTEGER NOT NULL,
			cpu_limit TEXT NOT NULL DEFAULT '',
			desired_state TEXT NOT NULL,
			quota_bytes INTEGER NOT NULL DEFAULT 0,
			quota_rx_baseline INTEGER NOT NULL DEFAULT 0,
			quota_tx_baseline INTEGER NOT NULL DEFAULT 0,
			quota_reset_at TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL,
			target_id TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL,
			source_ip TEXT NOT NULL DEFAULT '',
			details TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_created ON audit_events(created_at DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("database migration: %w", err)
		}
	}
	if err := s.ensurePolicyColumns(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, now()); err != nil {
		return fmt.Errorf("record database migration: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, ?)`, now()); err != nil {
		return fmt.Errorf("record policy migration: %w", err)
	}
	return nil
}

func (s *Store) ensurePolicyColumns(ctx context.Context) error {
	columns := map[string]string{
		"quota_bytes":       `ALTER TABLE managed_containers ADD COLUMN quota_bytes INTEGER NOT NULL DEFAULT 0`,
		"quota_rx_baseline": `ALTER TABLE managed_containers ADD COLUMN quota_rx_baseline INTEGER NOT NULL DEFAULT 0`,
		"quota_tx_baseline": `ALTER TABLE managed_containers ADD COLUMN quota_tx_baseline INTEGER NOT NULL DEFAULT 0`,
		"quota_reset_at":    `ALTER TABLE managed_containers ADD COLUMN quota_reset_at TEXT NOT NULL DEFAULT ''`,
		"expires_at":        `ALTER TABLE managed_containers ADD COLUMN expires_at TEXT NOT NULL DEFAULT ''`,
	}
	for column, statement := range columns {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('managed_containers') WHERE name=?`, column).Scan(&count); err != nil {
			return fmt.Errorf("inspect policy column %s: %w", column, err)
		}
		if count == 0 {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("add policy column %s: %w", column, err)
			}
		}
	}
	return nil
}

func (s *Store) UpsertContainer(ctx context.Context, container ManagedContainer) error {
	if strings.TrimSpace(container.Name) == "" {
		return errors.New("container name is required")
	}
	if container.DesiredState == "" {
		container.DesiredState = "running"
	}
	timestamp := now()
	if container.CreatedAt == "" {
		container.CreatedAt = timestamp
	}
	container.UpdatedAt = timestamp
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_containers(name,image,memory_limit_mb,disk_limit_gb,cpu_limit,desired_state,quota_bytes,quota_rx_baseline,quota_tx_baseline,quota_reset_at,expires_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET image=excluded.image,memory_limit_mb=excluded.memory_limit_mb,disk_limit_gb=excluded.disk_limit_gb,cpu_limit=excluded.cpu_limit,desired_state=excluded.desired_state,updated_at=excluded.updated_at`,
		container.Name, container.Image, container.MemoryLimitMb, container.DiskLimitGb, container.CPULimit, container.DesiredState, container.QuotaBytes, container.QuotaRxBaseline, container.QuotaTxBaseline, container.QuotaResetAt, container.ExpiresAt, container.CreatedAt, container.UpdatedAt)
	return err
}

func (s *Store) SetDesiredState(ctx context.Context, name, state string) error {
	if name == "" || (state != "running" && state != "stopped") {
		return errors.New("invalid managed container state")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_containers SET desired_state=?,updated_at=? WHERE name=?`, state, now(), name)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteContainer(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_containers WHERE name=?`, name)
	return err
}

func (s *Store) ListContainers(ctx context.Context) ([]ManagedContainer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,image,memory_limit_mb,disk_limit_gb,cpu_limit,desired_state,quota_bytes,quota_rx_baseline,quota_tx_baseline,quota_reset_at,expires_at,created_at,updated_at FROM managed_containers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedContainer, 0)
	for rows.Next() {
		var item ManagedContainer
		if err := rows.Scan(&item.Name, &item.Image, &item.MemoryLimitMb, &item.DiskLimitGb, &item.CPULimit, &item.DesiredState, &item.QuotaBytes, &item.QuotaRxBaseline, &item.QuotaTxBaseline, &item.QuotaResetAt, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpdatePolicy(ctx context.Context, name string, quotaBytes, quotaRxBaseline, quotaTxBaseline int64, quotaResetAt, expiresAt string) error {
	if name == "" || quotaBytes < 0 || quotaRxBaseline < 0 || quotaTxBaseline < 0 {
		return errors.New("invalid container policy")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_containers SET quota_bytes=?,quota_rx_baseline=?,quota_tx_baseline=?,quota_reset_at=?,expires_at=?,updated_at=? WHERE name=?`, quotaBytes, quotaRxBaseline, quotaTxBaseline, quotaResetAt, expiresAt, now(), name)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Export(ctx context.Context) (Backup, error) {
	containers, err := s.ListContainers(ctx)
	if err != nil {
		return Backup{}, err
	}
	return Backup{Version: 1, GeneratedAt: now(), Containers: containers}, nil
}

func (s *Store) Restore(ctx context.Context, backup Backup) error {
	if backup.Version != 1 {
		return fmt.Errorf("unsupported backup version %d", backup.Version)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_containers`); err != nil {
		return err
	}
	for _, item := range backup.Containers {
		if !managedNamePattern.MatchString(item.Name) || strings.TrimSpace(item.Image) == "" || (item.DesiredState != "running" && item.DesiredState != "stopped") || item.MemoryLimitMb < 16 || item.MemoryLimitMb > 8192 || item.DiskLimitGb < 1 || item.DiskLimitGb > 100 || item.QuotaBytes < 0 || item.QuotaRxBaseline < 0 || item.QuotaTxBaseline < 0 || (item.CPULimit != "" && !regexp.MustCompile(`^[1-9][0-9]{0,2}%$`).MatchString(item.CPULimit)) {
			return fmt.Errorf("invalid managed container %q in backup", item.Name)
		}
		if item.CreatedAt == "" {
			item.CreatedAt = now()
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = item.CreatedAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_containers(name,image,memory_limit_mb,disk_limit_gb,cpu_limit,desired_state,quota_bytes,quota_rx_baseline,quota_tx_baseline,quota_reset_at,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.Name, item.Image, item.MemoryLimitMb, item.DiskLimitGb, item.CPULimit, item.DesiredState, item.QuotaBytes, item.QuotaRxBaseline, item.QuotaTxBaseline, item.QuotaResetAt, item.ExpiresAt, item.CreatedAt, item.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Audit(ctx context.Context, event AuditEvent) error {
	if event.Actor == "" {
		event.Actor = "system"
	}
	if event.Result == "" {
		event.Result = "success"
	}
	if event.CreatedAt == "" {
		event.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(actor,action,target,target_id,result,source_ip,details,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		event.Actor, event.Action, event.Target, event.TargetID, event.Result, event.SourceIP, event.Details, event.CreatedAt)
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int, beforeID int64) ([]AuditEvent, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := `SELECT id,actor,action,target,target_id,result,source_ip,details,created_at FROM audit_events`
	args := []any{}
	if beforeID > 0 {
		query += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &event.Target, &event.TargetID, &event.Result, &event.SourceIP, &event.Details, &event.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func ParseBeforeID(value string) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return 0
	}
	return parsed
}
