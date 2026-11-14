//go:build windows

package interrupt

import (
	"os"
	"syscall"
)

// Go maps console close, logoff, and shutdown events to SIGTERM on Windows.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
