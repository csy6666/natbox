//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type hostSnapshot struct {
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	SwapTotalBytes       int64
	SwapAvailableBytes   int64
	DiskTotalBytes       int64
	DiskAvailableBytes   int64
}

func readHostSnapshot() (hostSnapshot, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return hostSnapshot{}, fmt.Errorf("read host memory: %w", err)
	}
	defer file.Close()
	values := map[string]int64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		if len(fields) > 2 && fields[2] == "kB" {
			value *= 1024
		}
		values[strings.TrimSuffix(fields[0], ":")] = value
	}
	if err := scanner.Err(); err != nil {
		return hostSnapshot{}, fmt.Errorf("read host memory: %w", err)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return hostSnapshot{}, fmt.Errorf("read host disk: %w", err)
	}
	blockSize := int64(stat.Bsize)
	return hostSnapshot{
		MemoryTotalBytes: values["MemTotal"], MemoryAvailableBytes: values["MemAvailable"],
		SwapTotalBytes: values["SwapTotal"], SwapAvailableBytes: values["SwapFree"],
		DiskTotalBytes: int64(stat.Blocks) * blockSize, DiskAvailableBytes: int64(stat.Bavail) * blockSize,
	}, nil
}
