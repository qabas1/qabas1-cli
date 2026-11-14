package cli

import (
	"testing"
)

func TestLogsUICommandsRegistered(t *testing.T) {
	root := NewRoot()
	cases := [][]string{
		{"logs", "ui"},
		{"logs", "ui", "up"},
		{"logs", "ui", "down"},
		{"logs", "ui", "status"},
		{"logs", "ui", "open"},
	}
	for _, path := range cases {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("Find %v: %v", path, err)
		}
		if cmd == nil {
			t.Fatalf("Find %v returned nil", path)
		}
	}

	up, _, err := root.Find([]string{"logs", "ui", "up"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"full", "all", "port"} {
		if up.Flags().Lookup(name) == nil {
			t.Fatalf("logs ui up missing --%s", name)
		}
	}
	down, _, err := root.Find([]string{"logs", "ui", "down"})
	if err != nil {
		t.Fatal(err)
	}
	if down.Flags().Lookup("wipe") == nil {
		t.Fatal("logs ui down missing --wipe")
	}
}

func TestLogsUIUpDryRun(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"logs", "ui", "up", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestLogsUIUpFullDryRun(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"logs", "ui", "up", "--full", "--dry-run", "--port", "8888"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestLogsUIDownDryRun(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"logs", "ui", "down", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}
