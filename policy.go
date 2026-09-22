package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"natbox/internal/incus"
	"natbox/internal/store"
)

func (a *app) policy(ctx context.Context, name string) (policyView, error) {
	items, err := a.store.ListContainers(ctx)
	if err != nil {
		return policyView{}, err
	}
	var item *store.ManagedContainer
	for index := range items {
		if items[index].Name == name {
			item = &items[index]
			break
		}
	}
	if item == nil {
		return policyView{}, errors.New("container is not managed by Natbox")
	}
	view := policyView{QuotaBytes: item.QuotaBytes, ExpiresAt: item.ExpiresAt, QuotaResetAt: item.QuotaResetAt, QuotaEnforced: item.QuotaBytes > 0, ExpiryEnforced: item.ExpiresAt != ""}
	stats, err := a.incus.Stats(ctx, name)
	if err == nil {
		view.UsedBytes = counterDelta(stats.RxBytes, item.QuotaRxBaseline) + counterDelta(stats.TxBytes, item.QuotaTxBaseline)
	}
	if view.QuotaBytes > view.UsedBytes {
		view.RemainingBytes = view.QuotaBytes - view.UsedBytes
	}
	return view, nil
}

func (a *app) updatePolicy(ctx context.Context, name string, request policyRequest) (policyView, error) {
	if request.QuotaBytes < 0 {
		return policyView{}, errors.New("quotaBytes must be zero or positive")
	}
	expiresAt := strings.TrimSpace(request.ExpiresAt)
	if expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return policyView{}, errors.New("expiresAt must be RFC3339")
		}
		expiresAt = parsed.UTC().Format(time.RFC3339)
	}
	stats, err := a.incus.Stats(ctx, name)
	if err != nil {
		return policyView{}, fmt.Errorf("read current counters: %w", err)
	}
	resetAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.store.UpdatePolicy(ctx, name, request.QuotaBytes, stats.RxBytes, stats.TxBytes, resetAt, expiresAt); err != nil {
		return policyView{}, err
	}
	return a.policy(ctx, name)
}

func (a *app) enforcePolicies(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	a.enforcePolicyPass(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.enforcePolicyPass(ctx)
		}
	}
}

func (a *app) enforcePolicyPass(ctx context.Context) {
	items, err := a.store.ListContainers(ctx)
	if err != nil {
		log.Printf("policy list: %v", err)
		return
	}
	runtimeItems, err := a.incus.List(ctx)
	if err != nil {
		log.Printf("policy runtime list: %v", err)
		return
	}
	runtimeByName := make(map[string]incus.Instance, len(runtimeItems))
	for _, item := range runtimeItems {
		runtimeByName[item.Name] = item
	}
	now := time.Now().UTC()
	for _, item := range items {
		if item.QuotaBytes <= 0 && item.ExpiresAt == "" {
			continue
		}
		runtimeItem, ok := runtimeByName[item.Name]
		if !ok || runtimeItem.Status != "Running" {
			continue
		}
		reason := ""
		if item.ExpiresAt != "" {
			if expiry, parseErr := time.Parse(time.RFC3339, item.ExpiresAt); parseErr == nil && !now.Before(expiry) {
				reason = "expired"
			}
		}
		if reason == "" && item.QuotaBytes > 0 {
			stats, statsErr := a.incus.Stats(ctx, item.Name)
			if statsErr != nil {
				log.Printf("policy stats for %s: %v", item.Name, statsErr)
				continue
			}
			used := counterDelta(stats.RxBytes, item.QuotaRxBaseline) + counterDelta(stats.TxBytes, item.QuotaTxBaseline)
			if used >= item.QuotaBytes {
				reason = fmt.Sprintf("quota reached (%d/%d bytes)", used, item.QuotaBytes)
			}
		}
		if reason == "" {
			continue
		}
		if err := a.incus.Stop(ctx, item.Name); err != nil {
			log.Printf("policy stop %s: %v", item.Name, err)
			a.recordSystemAudit("policy.stop", item.Name, "failure", reason+": "+err.Error())
			continue
		}
		if err := a.store.SetDesiredState(ctx, item.Name, "stopped"); err != nil {
			log.Printf("policy state %s: %v", item.Name, err)
		}
		a.recordSystemAudit("policy.stop", item.Name, "success", reason)
	}
}

func counterDelta(current, baseline int64) int64 {
	if current <= baseline {
		return 0
	}
	return current - baseline
}

func (a *app) recordSystemAudit(action, targetID, result, details string) {
	target := "container"
	if strings.HasPrefix(action, "backup.") {
		target = "database"
	}
	if err := a.store.Audit(context.Background(), store.AuditEvent{Actor: "system", Action: action, Target: target, TargetID: targetID, Result: result, Details: details}); err != nil {
		log.Printf("audit %s/%s: %v", action, targetID, err)
	}
}
