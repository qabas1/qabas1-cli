//go:build windows

package httpx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Winsock reports connection failures with WSA* codes that do not match the
// syscall.E* constants checked in retryableErr.
var winsockRetryable = []error{
	windows.WSAECONNRESET,
	windows.WSAECONNREFUSED,
	windows.WSAECONNABORTED,
	windows.WSAENETUNREACH,
	windows.WSAEHOSTUNREACH,
	windows.WSAENETRESET,
	windows.WSAETIMEDOUT,
}

func platformRetryable(err error) bool {
	for _, target := range winsockRetryable {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
