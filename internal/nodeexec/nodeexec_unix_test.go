//go:build !windows

package nodeexec

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalCancellationKillsProcessTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Local{Name: "local"}).Run(ctx, "sleep 30 & wait", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("descendant kept command pipes open after timeout")
	}
}
