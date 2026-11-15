package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

type ServiceStatus struct {
	Service     string `json:"service"`
	Name        string `json:"name"`
	ContainerID string `json:"containerId"`
	Image       string `json:"image"`
	Version     string `json:"version"`
	State       string `json:"state"`
	Health      string `json:"health"`
	Memory      string `json:"memory"`
}

func (r *Runtime) Ps(p paths.Paths, env []string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := r.dockerOut(p, env, []string{"ps"}, &stdout, &stderr); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// ServiceList is the compose ps listing without per-container stats (Memory is "-").
func (r *Runtime) ServiceList(p paths.Paths, env []string, version string) ([]ServiceStatus, error) {
	var stdout, stderr bytes.Buffer
	if err := r.dockerOut(p, env, []string{"ps", "--format", "json"}, &stdout, &stderr); err != nil {
		return nil, err
	}
	return parseComposePsJSON(stdout.String(), version)
}

func (r *Runtime) ServiceStatus(p paths.Paths, env []string, version string) ([]ServiceStatus, error) {
	services, err := r.ServiceList(p, env, version)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return services, nil
	}

	names := make([]string, 0, len(services))
	for _, svc := range services {
		if svc.Name != "" {
			names = append(names, svc.Name)
		}
	}
	stats, err := r.containerStats(env, names)
	if err != nil {
		for i := range services {
			services[i].Memory = "-"
		}
		return services, nil
	}
	for i := range services {
		if mem := stats[services[i].Name]; mem != "" {
			services[i].Memory = mem
		} else {
			services[i].Memory = "-"
		}
	}
	return services, nil
}

func (r *Runtime) containerStats(env []string, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	if _, err := r.LookPath("docker"); err != nil {
		return nil, exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{ json . }}"}, names...)
	var stdout, stderr bytes.Buffer
	if err := r.Run("docker", args, env, &stdout, &stderr); err != nil {
		msg := err.Error()
		if stderr.Len() > 0 {
			msg = strings.TrimSpace(stderr.String())
		}
		return nil, exitcode.New(exitcode.Network, "docker stats: %s", msg)
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		name := firstString(row, "Name", "Container")
		if name == "" {
			continue
		}
		out[name] = firstString(row, "MemUsage")
	}
	return out, nil
}

func parseComposePsJSON(raw string, version string) ([]ServiceStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rows []map[string]any
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, err
		}
	} else {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	services := make([]ServiceStatus, 0, len(rows))
	for _, row := range rows {
		id := firstString(row, "ID", "Id", "ContainerID")
		if len(id) > 12 {
			id = id[:12]
		}
		health := firstString(row, "Health")
		if health == "" {
			health = "-"
		}
		services = append(services, ServiceStatus{
			Service:     firstString(row, "Service"),
			Name:        firstString(row, "Name"),
			ContainerID: id,
			Image:       firstString(row, "Image"),
			Version:     version,
			State:       firstString(row, "State"),
			Health:      health,
			Memory:      "-",
		})
	}
	return services, nil
}

func firstString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			switch v := value.(type) {
			case string:
				return v
			case fmt.Stringer:
				return v.String()
			}
		}
	}
	return ""
}
