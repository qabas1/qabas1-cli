package exitcode_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
)

func TestCodeOf(t *testing.T) {
	if exitcode.CodeOf(nil) != 0 {
		t.Fatal("nil")
	}
	if exitcode.CodeOf(errors.New("x")) != exitcode.Generic {
		t.Fatal("generic")
	}
	err := exitcode.New(exitcode.Compatibility, "nope")
	if exitcode.CodeOf(err) != exitcode.Compatibility {
		t.Fatal("compat")
	}
	wrapped := fmt.Errorf("wrap: %w", err)
	if exitcode.CodeOf(exitcode.Wrap(exitcode.Generic, wrapped)) != exitcode.Compatibility {
		t.Fatal("wrap should preserve existing code")
	}
}

func TestHintOf(t *testing.T) {
	if exitcode.HintOf(nil) != "" {
		t.Fatal("nil hint")
	}
	err := exitcode.NewHint(exitcode.InvalidInput, "try --yes", "refusing")
	if exitcode.HintOf(err) != "try --yes" {
		t.Fatalf("hint=%q", exitcode.HintOf(err))
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatal("code")
	}
	if exitcode.HintOf(errors.New("x")) != "" {
		t.Fatal("plain error hint")
	}
}
