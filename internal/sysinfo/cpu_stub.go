//go:build !linux

package sysinfo

func ReadCPUStat() (CPUStat, []CPUStat, bool) { return CPUStat{}, nil, false }

func ReadTasks() (threads, running int, ok bool) { return 0, 0, false }
