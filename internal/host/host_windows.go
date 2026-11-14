//go:build windows

package host

import "golang.org/x/sys/windows"

func isPrivileged() bool { return windows.GetCurrentProcessToken().IsElevated() }
