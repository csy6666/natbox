package main

import (
	"context"
	"sort"
	"time"

	"natbox/internal/incus"
	"natbox/internal/store"
)

type reconcileReport struct {
	GeneratedAt       string                   `json:"generatedAt"`
	ManagedMissing    []store.ManagedContainer `json:"managedMissingRuntime"`
	RuntimeUnmanaged  []incus.Instance         `json:"runtimeUnmanaged"`
	DesiredStateDrift []stateDrift             `json:"desiredStateDrift"`
}

type stateDrift struct {
	Name          string `json:"name"`
	DesiredState  string `json:"desiredState"`
	RuntimeStatus string `json:"runtimeStatus"`
}

func (a *app) reconcile(ctx context.Context) (reconcileReport, error) {
	managed, err := a.store.ListContainers(ctx)
	if err != nil {
		return reconcileReport{}, err
	}
	runtimeItems, err := a.incus.List(ctx)
	if err != nil {
		return reconcileReport{}, err
	}
	managedByName := make(map[string]store.ManagedContainer, len(managed))
	for _, item := range managed {
		managedByName[item.Name] = item
	}
	runtimeByName := make(map[string]incus.Instance, len(runtimeItems))
	for _, item := range runtimeItems {
		runtimeByName[item.Name] = item
	}
	report := reconcileReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ManagedMissing: make([]store.ManagedContainer, 0), RuntimeUnmanaged: make([]incus.Instance, 0), DesiredStateDrift: make([]stateDrift, 0)}
	for _, item := range managed {
		runtimeItem, ok := runtimeByName[item.Name]
		if !ok {
			report.ManagedMissing = append(report.ManagedMissing, item)
			continue
		}
		desiredRunning := item.DesiredState == "running"
		actualRunning := runtimeItem.Status == "Running"
		if desiredRunning != actualRunning {
			report.DesiredStateDrift = append(report.DesiredStateDrift, stateDrift{Name: item.Name, DesiredState: item.DesiredState, RuntimeStatus: runtimeItem.Status})
		}
	}
	for _, item := range runtimeItems {
		if _, ok := managedByName[item.Name]; !ok {
			report.RuntimeUnmanaged = append(report.RuntimeUnmanaged, item)
		}
	}
	sort.Slice(report.ManagedMissing, func(i, j int) bool { return report.ManagedMissing[i].Name < report.ManagedMissing[j].Name })
	sort.Slice(report.RuntimeUnmanaged, func(i, j int) bool { return report.RuntimeUnmanaged[i].Name < report.RuntimeUnmanaged[j].Name })
	sort.Slice(report.DesiredStateDrift, func(i, j int) bool { return report.DesiredStateDrift[i].Name < report.DesiredStateDrift[j].Name })
	return report, nil
}
