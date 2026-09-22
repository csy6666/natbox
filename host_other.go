//go:build !linux

package main

import "errors"

type hostSnapshot struct {
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	SwapTotalBytes       int64
	SwapAvailableBytes   int64
	DiskTotalBytes       int64
	DiskAvailableBytes   int64
}

func readHostSnapshot() (hostSnapshot, error) {
	return hostSnapshot{}, errors.New("host capacity is only supported on Linux")
}
