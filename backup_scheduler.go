package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *app) startBackupScheduler(ctx context.Context) {
	directory := strings.TrimSpace(os.Getenv("NATBOX_BACKUP_DIR"))
	if directory == "" {
		return
	}
	interval := time.Duration(envInt("NATBOX_BACKUP_INTERVAL_MIN", 360)) * time.Minute
	if interval < time.Minute {
		interval = time.Minute
	}
	retention := envInt("NATBOX_BACKUP_RETENTION", 7)
	if retention < 1 {
		retention = 1
	}
	go func() {
		a.backupOnce(ctx, directory, retention)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.backupOnce(ctx, directory, retention)
			}
		}
	}()
}

func (a *app) backupOnce(ctx context.Context, directory string, retention int) {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		log.Printf("automatic backup directory: %v", err)
		return
	}
	filename := fmt.Sprintf("natbox-%s-%d.db", time.Now().UTC().Format("20060102T150405Z"), time.Now().UnixNano())
	destination := filepath.Join(directory, filename)
	if err := a.store.BackupDatabase(ctx, destination); err != nil {
		log.Printf("automatic database backup: %v", err)
		a.recordSystemAudit("backup.create", "", "failure", err.Error())
		return
	}
	a.recordSystemAudit("backup.create", "", "success", destination)
	a.pruneBackups(directory, retention)
}

func (a *app) pruneBackups(directory string, retention int) {
	paths, err := filepath.Glob(filepath.Join(directory, "natbox-*.db"))
	if err != nil {
		log.Printf("automatic backup scan: %v", err)
		return
	}
	sort.Strings(paths)
	if len(paths) <= retention {
		return
	}
	for _, path := range paths[:len(paths)-retention] {
		if err := os.Remove(path); err != nil {
			log.Printf("automatic backup prune %s: %v", path, err)
		}
	}
}

func backupRetentionDescription() string {
	return strconv.Itoa(envInt("NATBOX_BACKUP_RETENTION", 7))
}
