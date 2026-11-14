//go:build !linux && !windows

package sysinfo

import "errors"

var errUnsupported = errors.New("sysinfo: unsupported platform")

func platformInfo() Platform { return Platform{} }

func ReadCPUTimes() (CPUTimes, bool) { return CPUTimes{}, false }

func memoryInfo() (Memory, error) { return Memory{}, errUnsupported }

func DiskUsage(string) (Disk, error) { return Disk{}, errUnsupported }
