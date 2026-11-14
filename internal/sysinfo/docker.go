package sysinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Docker struct {
	Version          string        `json:"version,omitempty"`
	OSType           string        `json:"osType,omitempty"`
	RootDir          string        `json:"rootDir,omitempty"`
	NCPU             int           `json:"ncpu,omitempty"`
	MemTotalBytes    uint64        `json:"memTotalBytes,omitempty"`
	ContainersTotal  int           `json:"containersTotal"`
	ContainersUp     int           `json:"containersRunning"`
	Usage            []DockerUsage `json:"usage,omitempty"`
	ReclaimableBytes uint64        `json:"reclaimableBytes"`
}

type DockerUsage struct {
	Type             string `json:"type"`
	Count            int    `json:"count"`
	Active           int    `json:"active"`
	SizeBytes        uint64 `json:"sizeBytes"`
	ReclaimableBytes uint64 `json:"reclaimableBytes"`
}

const dockerTimeout = 15 * time.Second

func dockerOutput(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Output()
}

// DockerMemTotal is the RAM available to containers (the Docker Desktop VM on
// Windows/macOS, the host on Linux), or 0 when Docker is unreachable.
func DockerMemTotal() uint64 {
	raw, err := dockerOutput("info", "--format", "{{.MemTotal}}")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

func dockerInfo() *Docker {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil
	}
	raw, err := dockerOutput("info", "--format", "{{json .}}")
	if err != nil {
		return nil
	}
	d, err := ParseDockerInfo(raw)
	if err != nil {
		return nil
	}
	if df, err := dockerOutput("system", "df", "--format", "{{json .}}"); err == nil {
		d.Usage = ParseSystemDF(df)
		for _, u := range d.Usage {
			d.ReclaimableBytes += u.ReclaimableBytes
		}
	}
	return d
}

// ParseDockerInfo decodes `docker info --format '{{json .}}'`.
func ParseDockerInfo(raw []byte) (*Docker, error) {
	var v struct {
		ServerVersion     string
		OSType            string
		DockerRootDir     string
		NCPU              int
		MemTotal          uint64
		Containers        int
		ContainersRunning int
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &Docker{
		Version:         v.ServerVersion,
		OSType:          v.OSType,
		RootDir:         v.DockerRootDir,
		NCPU:            v.NCPU,
		MemTotalBytes:   v.MemTotal,
		ContainersTotal: v.Containers,
		ContainersUp:    v.ContainersRunning,
	}, nil
}

// ParseSystemDF decodes `docker system df --format '{{json .}}'` (one JSON
// object per line with human-readable sizes such as "1.2GB (50%)").
func ParseSystemDF(raw []byte) []DockerUsage {
	var out []DockerUsage
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v struct {
			Type        string
			TotalCount  string
			Active      string
			Size        string
			Reclaimable string
		}
		if json.Unmarshal([]byte(line), &v) != nil || v.Type == "" {
			continue
		}
		count, _ := strconv.Atoi(strings.TrimSpace(v.TotalCount))
		active, _ := strconv.Atoi(strings.TrimSpace(v.Active))
		recl, _, _ := strings.Cut(v.Reclaimable, " ")
		out = append(out, DockerUsage{
			Type:             v.Type,
			Count:            count,
			Active:           active,
			SizeBytes:        ParseSize(v.Size),
			ReclaimableBytes: ParseSize(recl),
		})
	}
	return out
}

// ParseSize parses Docker's human sizes ("0B", "12.5kB", "1.2GB", "3GiB").
func ParseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mult := map[string]float64{
		"": 1, "b": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}[unit]
	if mult == 0 {
		return 0
	}
	return uint64(n * mult)
}
