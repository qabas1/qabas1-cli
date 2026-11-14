package sysinfo

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const meminfo = `MemTotal:       16000000 kB
MemFree:         1000000 kB
MemAvailable:    4000000 kB
Buffers:          100000 kB
Cached:          2000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
`

func TestParseMeminfo(t *testing.T) {
	m := ParseMeminfo(meminfo)
	if m.TotalBytes != 16000000*1024 || m.AvailableBytes != 4000000*1024 {
		t.Fatalf("got %+v", m)
	}
	if m.SwapTotalBytes != 2000000*1024 || m.SwapFreeBytes != 1500000*1024 {
		t.Fatalf("swap %+v", m)
	}
	if m.UsedPercent != 75 {
		t.Fatalf("used %v", m.UsedPercent)
	}
	old := ParseMeminfo(strings.Replace(meminfo, "MemAvailable:    4000000 kB\n", "", 1))
	if old.AvailableBytes != (1000000+100000+2000000)*1024 {
		t.Fatalf("fallback available %d", old.AvailableBytes)
	}
}

func TestParseOSReleaseCPUAndLoad(t *testing.T) {
	if got := ParseOSRelease("NAME=\"Ubuntu\"\nVERSION=\"24.04 LTS\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"); got != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("os-release %q", got)
	}
	if got := ParseOSRelease("NAME=Debian\nVERSION=\"12 (bookworm)\"\n"); got != "Debian 12 (bookworm)" {
		t.Fatalf("os-release fallback %q", got)
	}
	if got := ParseCPUModel("processor\t: 0\nmodel name\t: AMD EPYC 7B13\n"); got != "AMD EPYC 7B13" {
		t.Fatalf("cpu %q", got)
	}
	if got := ParseCPUModel("processor\t: 0\nModel\t\t: Raspberry Pi 5\n"); got != "Raspberry Pi 5" {
		t.Fatalf("arm cpu %q", got)
	}
	l := ParseLoadavg("0.52 0.41 0.30 1/234 5678\n")
	if len(l) != 3 || l[0] != 0.52 || l[2] != 0.30 {
		t.Fatalf("load %v", l)
	}
	if ParseLoadavg("garbage") != nil {
		t.Fatal("expected nil load")
	}
}

func TestParseDocker(t *testing.T) {
	d, err := ParseDockerInfo([]byte(`{"ServerVersion":"27.3.1","OSType":"linux","DockerRootDir":"/var/lib/docker","NCPU":4,"MemTotal":8589934592,"Containers":12,"ContainersRunning":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "27.3.1" || d.NCPU != 4 || d.MemTotalBytes != 8<<30 || d.ContainersUp != 10 || d.RootDir != "/var/lib/docker" {
		t.Fatalf("info %+v", d)
	}
	usage := ParseSystemDF([]byte(`{"Active":"5","Reclaimable":"1.2GB (50%)","Size":"2.4GB","TotalCount":"9","Type":"Images"}
{"Active":"0","Reclaimable":"512MB","Size":"512MB","TotalCount":"40","Type":"Build Cache"}
`))
	if len(usage) != 2 || usage[0].Count != 9 || usage[0].ReclaimableBytes != 1.2e9 || usage[1].SizeBytes != 512e6 {
		t.Fatalf("usage %+v", usage)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"0B": 0, "12kB": 12e3, "1.5GB": 1.5e9, "3GiB": 3 << 30, "": 0, "abc": 0, "7XB": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func fakeProbes() Probes {
	return Probes{
		GOOS: "linux", GOARCH: "amd64", NumCPU: 8,
		Now:      func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
		Hostname: func() (string, error) { return "node1", nil },
		Platform: func() Platform {
			return Platform{OSName: "Ubuntu 24.04", Kernel: "6.8.0", CPUModel: "EPYC", Uptime: 50 * time.Hour, Load: []float64{1, 2, 3}}
		},
		Memory: func() (Memory, error) {
			return Memory{TotalBytes: 16 << 30, AvailableBytes: 8 << 30, UsedPercent: 50}, nil
		},
		Disk: func(path string) (Disk, error) {
			if path == "/missing" {
				return Disk{}, errors.New("nope")
			}
			if path == "/var/lib/docker" {
				return Disk{Path: path, TotalBytes: 500 << 30, FreeBytes: 10 << 30, UsedPercent: 98}, nil
			}
			return Disk{Path: path, TotalBytes: 100 << 30, FreeBytes: 50 << 30, UsedPercent: 50}, nil
		},
		Docker: func() *Docker { return &Docker{Version: "27", RootDir: "/var/lib/docker", ReclaimableBytes: 6 << 30} },
		Addrs:  func() []string { return []string{"10.0.0.5"} },
	}
}

func TestCollectWith(t *testing.T) {
	info := CollectWith(fakeProbes(),
		Target{Label: "system", Path: "/"},
		Target{Label: "state", Path: "/var/lib/qabasone"},
		Target{Label: "gone", Path: "/missing"},
	)
	if info.Hostname != "node1" || info.CPU.Cores != 8 || info.CPU.Model != "EPYC" || info.UptimeSec != 50*3600 {
		t.Fatalf("info %+v", info)
	}
	if info.BootTime != time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) {
		t.Fatalf("boot %s", info.BootTime)
	}
	if len(info.Disks) != 2 || info.Disks[0].Label != "system,state" || info.Disks[1].Label != "docker" {
		t.Fatalf("disks %+v", info.Disks)
	}
	levels := map[string]string{}
	for _, f := range info.Findings {
		levels[f.Check] = f.Level
	}
	if levels["memory"] != LevelOK || levels["disk:system,state"] != LevelOK || levels["disk:docker"] != LevelCrit || levels["docker-disk"] != LevelWarn {
		t.Fatalf("findings %+v", info.Findings)
	}
	if Worst(info.Findings) != LevelCrit {
		t.Fatal("worst should be crit")
	}
}

func TestEvaluateThresholds(t *testing.T) {
	cases := []struct {
		avail, total uint64
		want         string
	}{
		{50, 100, LevelOK},
		{9, 100, LevelWarn},
		{4, 100, LevelCrit},
	}
	for _, c := range cases {
		fs := Evaluate(Info{Memory: &Memory{TotalBytes: c.total << 30, AvailableBytes: c.avail << 30}})
		if fs[0].Level != c.want {
			t.Errorf("mem %d/%d = %s, want %s", c.avail, c.total, fs[0].Level, c.want)
		}
	}
	small := DiskFinding(Disk{Label: "state", TotalBytes: 40 << 30, FreeBytes: 8 << 30, UsedPercent: 80})
	if small.Level != LevelWarn {
		t.Fatalf("below min free should warn: %+v", small)
	}
	inodes := DiskFinding(Disk{Label: "state", TotalBytes: 400 << 30, FreeBytes: 300 << 30, UsedPercent: 25, InodesTotal: 1000, InodesFree: 10})
	if inodes.Level != LevelCrit {
		t.Fatalf("inode exhaustion should be crit: %+v", inodes)
	}
	win := Evaluate(Info{OS: "windows", Docker: &Docker{MemTotalBytes: 1 << 30}})
	if win[0].Level != LevelWarn || !strings.Contains(win[0].Detail, "Docker VM") {
		t.Fatalf("docker vm memory %+v", win)
	}
}

func TestHumanFormatting(t *testing.T) {
	if HumanBytes(512) != "512 B" || HumanBytes(1536) != "1.5 KiB" || HumanBytes(5<<30) != "5.0 GiB" {
		t.Fatalf("bytes %s %s %s", HumanBytes(512), HumanBytes(1536), HumanBytes(5<<30))
	}
	if HumanDuration(26*time.Hour+5*time.Minute) != "1d 2h 5m" || HumanDuration(0) != "-" || HumanDuration(5*time.Minute) != "5m" {
		t.Fatal("duration formatting")
	}
}

func TestDiskUsageRealVolume(t *testing.T) {
	d, err := DiskUsage(t.TempDir() + "/not/yet/created")
	if err != nil {
		t.Skipf("disk probe unsupported here: %v", err)
	}
	if d.TotalBytes == 0 || d.FreeBytes > d.TotalBytes {
		t.Fatalf("disk %+v", d)
	}
}
