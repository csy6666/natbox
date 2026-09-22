package main

import (
	"context"
	"fmt"
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
	PortForwardIssues []portForwardIssue       `json:"portForwardIssues"`
}

type stateDrift struct {
	Name          string `json:"name"`
	DesiredState  string `json:"desiredState"`
	RuntimeStatus string `json:"runtimeStatus"`
}

type portForwardIssue struct {
	Kind      string `json:"kind"`
	Protocol  string `json:"protocol,omitempty"`
	Port      int    `json:"port,omitempty"`
	Container string `json:"container,omitempty"`
	Other     string `json:"other,omitempty"`
	Details   string `json:"details"`
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
	report := reconcileReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ManagedMissing: make([]store.ManagedContainer, 0), RuntimeUnmanaged: make([]incus.Instance, 0), DesiredStateDrift: make([]stateDrift, 0), PortForwardIssues: make([]portForwardIssue, 0)}
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
	usedPorts := make(map[string]string)
	for _, item := range runtimeItems {
		forwards, forwardErr := a.incus.ListPortForwards(ctx, item.Name)
		if forwardErr != nil {
			report.PortForwardIssues = append(report.PortForwardIssues, portForwardIssue{Kind: "unreadable", Container: item.Name, Details: forwardErr.Error()})
			continue
		}
		for _, forward := range forwards {
			key := fmt.Sprintf("%s/%d", forward.Protocol, forward.ListenPort)
			if other, exists := usedPorts[key]; exists && other != item.Name {
				report.PortForwardIssues = append(report.PortForwardIssues, portForwardIssue{Kind: "duplicate_listen_port", Protocol: forward.Protocol, Port: forward.ListenPort, Container: item.Name, Other: other, Details: "same public listen port is assigned to multiple containers"})
			} else {
				usedPorts[key] = item.Name
			}
		}
	}
	sort.Slice(report.ManagedMissing, func(i, j int) bool { return report.ManagedMissing[i].Name < report.ManagedMissing[j].Name })
	sort.Slice(report.RuntimeUnmanaged, func(i, j int) bool { return report.RuntimeUnmanaged[i].Name < report.RuntimeUnmanaged[j].Name })
	sort.Slice(report.DesiredStateDrift, func(i, j int) bool { return report.DesiredStateDrift[i].Name < report.DesiredStateDrift[j].Name })
	sort.Slice(report.PortForwardIssues, func(i, j int) bool {
		if report.PortForwardIssues[i].Protocol == report.PortForwardIssues[j].Protocol {
			return report.PortForwardIssues[i].Port < report.PortForwardIssues[j].Port
		}
		return report.PortForwardIssues[i].Protocol < report.PortForwardIssues[j].Protocol
	})
	return report, nil
}
