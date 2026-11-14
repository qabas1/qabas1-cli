package host

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
)

type Report struct {
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	Linux       bool     `json:"linux"`
	Supported   bool     `json:"supported"`
	Privileged  bool     `json:"privileged"`
	Docker      bool     `json:"docker"`
	Compose     bool     `json:"compose"`
	DockerVer   string   `json:"dockerVersion,omitempty"`
	EngineOS    string   `json:"engineOS,omitempty"`
	CPUs        int      `json:"cpus"`
	MemoryMiB   uint64   `json:"memoryMiB,omitempty"`
	DiskFreeMiB uint64   `json:"diskFreeMiB,omitempty"`
	Errors      []string `json:"errors,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

const (
	minCPUs      = 2
	minMemoryMiB = 2048
	minDiskMiB   = 10240
)

// Probes are the host/Docker lookups Inspect relies on. Injectable for tests.
type Probes struct {
	GOOS          string
	GOARCH        string
	CPUs          int
	Privileged    func() bool
	HasDocker     func() bool
	DockerVersion func() string
	ComposeOK     func() bool
	EngineOSType  func() string
	MemoryMiB     func() uint64
	DiskFreeMiB   func() uint64
}

func defaultProbes() Probes {
	return Probes{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		CPUs:          runtime.NumCPU(),
		Privileged:    isPrivileged,
		HasDocker:     func() bool { p, err := exec.LookPath("docker"); return err == nil && p != "" },
		DockerVersion: dockerVersion,
		ComposeOK:     dockerComposeOK,
		EngineOSType:  dockerEngineOSType,
		MemoryMiB:     memoryMiB,
		DiskFreeMiB:   func() uint64 { return diskFreeMiB(paths.Resolve().StateDir) },
	}
}

func memoryMiB() uint64 {
	m, err := sysinfo.DefaultProbes().Memory()
	if err != nil {
		return 0
	}
	return m.TotalBytes >> 20
}

func diskFreeMiB(dir string) uint64 {
	d, err := sysinfo.DiskUsage(dir)
	if err != nil {
		return 0
	}
	return d.FreeBytes >> 20
}

func Inspect() Report { return InspectWith(defaultProbes()) }

// InspectWith builds a Report from the given probes. Linux hosts run Docker
// natively; Windows hosts are supported through Docker Desktop in Linux
// containers mode.
func InspectWith(pr Probes) Report {
	r := Report{
		OS:    pr.GOOS,
		Arch:  pr.GOARCH,
		Linux: pr.GOOS == "linux",
		CPUs:  pr.CPUs,
	}
	windows := pr.GOOS == "windows"
	r.Supported = r.Linux || windows
	switch pr.GOARCH {
	case "amd64", "arm64":
	default:
		r.Errors = append(r.Errors, fmt.Sprintf("unsupported architecture %s (need amd64 or arm64)", pr.GOARCH))
	}
	if !r.Supported {
		r.Errors = append(r.Errors, fmt.Sprintf("QabasOne install requires Linux or Windows with Docker Desktop (got %s)", pr.GOOS))
	}
	r.Privileged = pr.Privileged()
	if !r.Privileged {
		switch {
		case r.Linux:
			r.Warnings = append(r.Warnings, "not running as root; Docker access may fail")
		case windows:
			r.Errors = append(r.Errors, "run qabas from an elevated PowerShell (Run as administrator); it writes under %ProgramData%\\QabasOne")
		}
	}
	if pr.HasDocker() {
		r.Docker = true
		r.DockerVer = pr.DockerVersion()
		r.Compose = pr.ComposeOK()
		if !r.Compose {
			r.Errors = append(r.Errors, "docker compose plugin is required")
		}
		if !r.Linux {
			r.EngineOS = pr.EngineOSType()
			switch r.EngineOS {
			case "linux":
			case "":
				r.Errors = append(r.Errors, "docker daemon is not reachable; start Docker Desktop and wait until it is running")
			default:
				r.Errors = append(r.Errors, fmt.Sprintf("docker engine runs %s containers; switch Docker Desktop to Linux containers", r.EngineOS))
			}
		}
	} else {
		r.Errors = append(r.Errors, "docker is not installed")
	}
	if r.CPUs < minCPUs {
		r.Warnings = append(r.Warnings, fmt.Sprintf("recommended at least %d CPUs (have %d)", minCPUs, r.CPUs))
	}
	if pr.MemoryMiB != nil {
		r.MemoryMiB = pr.MemoryMiB()
		if r.MemoryMiB > 0 && r.MemoryMiB < minMemoryMiB {
			r.Warnings = append(r.Warnings, fmt.Sprintf("recommended at least %d MiB RAM (have %d MiB)", minMemoryMiB, r.MemoryMiB))
		}
	}
	if pr.DiskFreeMiB != nil {
		r.DiskFreeMiB = pr.DiskFreeMiB()
		if r.DiskFreeMiB > 0 && r.DiskFreeMiB < minDiskMiB {
			r.Warnings = append(r.Warnings, fmt.Sprintf("recommended at least %d MiB free disk for QabasOne data (have %d MiB)", minDiskMiB, r.DiskFreeMiB))
		}
	}
	return r
}

// LinuxEngine reports whether Docker runs Linux containers (native Linux host
// or Docker Desktop in Linux containers mode).
func (r Report) LinuxEngine() bool {
	if r.Linux {
		return r.Docker
	}
	return r.EngineOS == "linux"
}

func (r Report) RequireLinux() error {
	if r.Linux {
		return nil
	}
	return exitcode.New(exitcode.InvalidInput, "%s", strings.Join(r.Errors, "; "))
}

func (r Report) RequireReady() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return exitcode.New(exitcode.InvalidInput, "%s", strings.Join(r.Errors, "; "))
}

func dockerVersion() string {
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dockerComposeOK() bool {
	err := exec.Command("docker", "compose", "version").Run()
	return err == nil
}

func dockerEngineOSType() string {
	out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").Output()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

func ParseMiB(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}
