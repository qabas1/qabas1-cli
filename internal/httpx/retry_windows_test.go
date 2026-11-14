//go:build windows

package httpx

import (
	"fmt"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRetryableErrWinsock(t *testing.T) {
	for _, errno := range []windows.Errno{windows.WSAECONNRESET, windows.WSAECONNREFUSED, windows.WSAECONNABORTED} {
		err := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", errno)}
		if !retryableErr(fmt.Errorf("get: %w", err)) {
			t.Fatalf("expected %v to be retryable", errno)
		}
	}
	if retryableErr(fmt.Errorf("plain")) {
		t.Fatal("plain error must not be retryable")
	}
}
