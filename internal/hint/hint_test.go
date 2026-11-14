package hint

import "testing"

func TestCmdFor(t *testing.T) {
	tests := []struct {
		goos    string
		keepEnv bool
		want    string
	}{
		{"linux", false, "sudo qabas install"},
		{"linux", true, "sudo -E qabas install"},
		{"windows", false, "qabas install"},
		{"windows", true, "qabas install"},
	}
	for _, tt := range tests {
		if got := cmdFor(tt.goos, "qabas install", tt.keepEnv); got != tt.want {
			t.Errorf("cmdFor(%s, %v) = %q, want %q", tt.goos, tt.keepEnv, got, tt.want)
		}
	}
}

func TestRunFor(t *testing.T) {
	if got, want := runFor("linux", "qabas install"), "run `sudo qabas install`"; got != want {
		t.Fatalf("linux: %q, want %q", got, want)
	}
	if got, want := runFor("windows", "qabas install"), "run `qabas install`"+ElevatedNote; got != want {
		t.Fatalf("windows: %q, want %q", got, want)
	}
}

func TestLocalizeFor(t *testing.T) {
	tests := []struct {
		goos, in, want string
	}{
		{"linux", "run `sudo -E qabas auth login`", "run `sudo -E qabas auth login`"},
		{"windows", "run `sudo -E qabas auth login --username X`", "run `qabas auth login --username X`"},
		{"windows", "  sudo qabas deploy dev\n  sudo qabas install", "  qabas deploy dev\n  qabas install"},
		{"windows", `export GITHUB_TOKEN="$(gh auth token)" && sudo -E qabas auth login`, `$env:GITHUB_TOKEN = (gh auth token); qabas auth login`},
		{"windows", "remove stale lock: sudo rm /x", "remove stale lock: Remove-Item /x"},
		{"windows", "", ""},
	}
	for _, tt := range tests {
		if got := localizeFor(tt.goos, tt.in); got != tt.want {
			t.Errorf("localizeFor(%s, %q) = %q, want %q", tt.goos, tt.in, got, tt.want)
		}
	}
}
