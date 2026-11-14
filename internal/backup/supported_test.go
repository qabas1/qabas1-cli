package backup

import (
	"runtime"
	"testing"
)

func TestSupportedOS(t *testing.T) {
	if !supportedOS("linux") {
		t.Fatal("linux must support vpg backups")
	}
	if supportedOS("windows") {
		t.Fatal("windows must not support vpg backups")
	}
	err := RequireSupported()
	if runtime.GOOS == "windows" && err == nil {
		t.Fatal("expected RequireSupported to fail on windows")
	}
	if runtime.GOOS != "windows" && err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
