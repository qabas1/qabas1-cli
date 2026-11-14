package host

import (
	"strings"
	"testing"
)

func fakeProbes(goos string, privileged bool, engine string) Probes {
	return Probes{
		GOOS:          goos,
		GOARCH:        "amd64",
		CPUs:          4,
		Privileged:    func() bool { return privileged },
		HasDocker:     func() bool { return true },
		DockerVersion: func() string { return "27.3.1" },
		ComposeOK:     func() bool { return true },
		EngineOSType:  func() string { return engine },
	}
}

func TestInspectWith(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		privileged bool
		engine     string
		wantReady  bool
		wantErr    string
		linuxEng   bool
	}{
		{name: "linux root", goos: "linux", privileged: true, wantReady: true, linuxEng: true},
		{name: "linux non-root warns only", goos: "linux", privileged: false, wantReady: true, linuxEng: true},
		{name: "windows elevated linux engine", goos: "windows", privileged: true, engine: "linux", wantReady: true, linuxEng: true},
		{name: "windows not elevated", goos: "windows", privileged: false, engine: "linux", wantErr: "elevated PowerShell", linuxEng: true},
		{name: "windows containers mode", goos: "windows", privileged: true, engine: "windows", wantErr: "switch Docker Desktop to Linux containers"},
		{name: "docker desktop stopped", goos: "windows", privileged: true, engine: "", wantErr: "start Docker Desktop"},
		{name: "darwin unsupported", goos: "darwin", privileged: true, engine: "linux", wantErr: "requires Linux or Windows", linuxEng: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := InspectWith(fakeProbes(tt.goos, tt.privileged, tt.engine))
			err := r.RequireReady()
			if tt.wantReady && err != nil {
				t.Fatalf("expected ready, got %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
			if got := r.LinuxEngine(); got != tt.linuxEng {
				t.Fatalf("LinuxEngine = %v, want %v", got, tt.linuxEng)
			}
		})
	}
}

func TestInspectWithDockerMissing(t *testing.T) {
	pr := fakeProbes("windows", true, "linux")
	pr.HasDocker = func() bool { return false }
	r := InspectWith(pr)
	if err := r.RequireReady(); err == nil || !strings.Contains(err.Error(), "docker is not installed") {
		t.Fatalf("expected docker missing error, got %v", err)
	}
}
