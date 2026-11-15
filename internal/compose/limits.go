package compose

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

// LimitsOverlay is the CLI-owned compose overlay that bounds container
// memory and process counts and removes privileges services never need.
const LimitsOverlay = "compose.qabas-limits.yaml"

const (
	// EnvLimits=off disables the overlay; EnvLimitPrefix+SERVICE (e.g.
	// QABAS_LIMIT_POSTGRES=4g or =none) overrides one service's memory cap.
	EnvLimits      = "QABAS_LIMITS"
	EnvLimitPrefix = "QABAS_LIMIT_"

	DefaultPidsLimit  = 512
	PostgresPidsLimit = 1024
	NofileLimit       = 65536
)

// DroppedCaps are default Docker capabilities no QabasOne service uses.
// Services that declare cap_drop/cap_add upstream (most drop ALL) are left alone.
var DroppedCaps = []string{"NET_RAW", "MKNOD", "SYS_CHROOT", "AUDIT_WRITE"}

// Budget is a service's share of container RAM with a floor and ceiling.
type Budget struct {
	Percent       int
	Floor, Ceil   uint64
	MatchContains string
	MatchExact    []string
}

const mib = 1 << 20

// budgets are checked in order; the first match wins.
var budgets = []Budget{
	{MatchExact: []string{"postgres", "db"}, Percent: 25, Floor: 512 * mib, Ceil: 16384 * mib},
	{MatchExact: []string{"api"}, Percent: 15, Floor: 384 * mib, Ceil: 4096 * mib},
	{MatchContains: "worker", Percent: 8, Floor: 256 * mib, Ceil: 2048 * mib},
	{MatchExact: []string{"redis"}, Percent: 10, Floor: 128 * mib, Ceil: 2048 * mib},
	{MatchExact: []string{"minio"}, Percent: 10, Floor: 256 * mib, Ceil: 4096 * mib},
	{MatchExact: []string{"gotenberg"}, Percent: 8, Floor: 512 * mib, Ceil: 2048 * mib},
	{MatchExact: []string{"jsreport"}, Percent: 12, Floor: 1024 * mib, Ceil: 4096 * mib},
	{MatchExact: []string{"app", "dash", "siraj", "edge", "nginx", "web"}, Percent: 5, Floor: 128 * mib, Ceil: 1024 * mib},
}

func budgetFor(service string) (Budget, bool) {
	for _, b := range budgets {
		for _, e := range b.MatchExact {
			if service == e {
				return b, true
			}
		}
		if b.MatchContains != "" && strings.Contains(service, b.MatchContains) {
			return b, true
		}
	}
	return Budget{}, false
}

// ServiceLimits is what the overlay sets for one service.
type ServiceLimits struct {
	Service     string   `json:"service"`
	MemoryBytes uint64   `json:"memoryBytes,omitempty"`
	MemorySrc   string   `json:"memorySource,omitempty"`
	PidsLimit   int      `json:"pidsLimit,omitempty"`
	NoNewPrivs  bool     `json:"noNewPrivileges"`
	Nofile      int      `json:"nofile,omitempty"`
	CapDrop     []string `json:"capDrop,omitempty"`
	// Upstream lists keys left alone because the release already sets them.
	Upstream []string `json:"upstream,omitempty"`
}

// LimitsDisabled reports whether the operator turned limits off.
func LimitsDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvLimits)))
	return v == "off" || v == "0" || v == "false" || v == "no"
}

// PlanLimits computes limits for services from their merged definitions.
// ramBytes is the RAM available to containers (0 skips memory caps).
func PlanLimits(defs map[string]map[string]any, ramBytes uint64) []ServiceLimits {
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []ServiceLimits
	for _, name := range names {
		def := defs[name]
		if isOneOff(name, def) {
			continue
		}
		l := ServiceLimits{Service: name}
		if has(def, "mem_limit") || hasPath(def, "deploy", "resources", "limits", "memory") {
			l.Upstream = append(l.Upstream, "memory")
		} else if mem, src := memoryFor(name, ramBytes); mem > 0 {
			l.MemoryBytes, l.MemorySrc = mem, src
		}
		if has(def, "pids_limit") || hasPath(def, "deploy", "resources", "limits", "pids") {
			l.Upstream = append(l.Upstream, "pids_limit")
		} else if name == "postgres" || name == "db" {
			l.PidsLimit = PostgresPidsLimit
		} else {
			l.PidsLimit = DefaultPidsLimit
		}
		if has(def, "security_opt") || has(def, "privileged") {
			l.Upstream = append(l.Upstream, "security_opt")
		} else {
			l.NoNewPrivs = true
		}
		if hasPath(def, "ulimits", "nofile") {
			l.Upstream = append(l.Upstream, "ulimits")
		} else {
			l.Nofile = NofileLimit
		}
		if has(def, "cap_drop") || has(def, "cap_add") || has(def, "privileged") {
			l.Upstream = append(l.Upstream, "capabilities")
		} else {
			l.CapDrop = DroppedCaps
		}
		out = append(out, l)
	}
	return out
}

// isOneOff skips migration/seed jobs: they run briefly with their own sizing.
func isOneOff(name string, def map[string]any) bool {
	if strings.Contains(name, "migrate") || strings.Contains(name, "seed") || strings.HasSuffix(name, "-init") {
		return true
	}
	if r, ok := def["restart"].(string); ok && r == "no" {
		return true
	}
	return false
}

func memoryFor(service string, ram uint64) (uint64, string) {
	key := EnvLimitPrefix + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service))
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if strings.EqualFold(v, "none") || v == "0" {
			return 0, key
		}
		if n := parseMem(v); n > 0 {
			return n, key
		}
	}
	b, ok := budgetFor(service)
	if !ok || ram == 0 {
		return 0, ""
	}
	n := ram * uint64(b.Percent) / 100
	n = max(n, b.Floor)
	n = min(n, b.Ceil)
	return n / mib * mib, fmt.Sprintf("%d%% of RAM", b.Percent)
}

// parseMem reads 512m, 4g, 1.5g, or plain bytes.
func parseMem(s string) uint64 {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := float64(1)
	switch {
	case strings.HasSuffix(s, "g"), strings.HasSuffix(s, "gb"):
		mult = 1 << 30
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "mb"):
		mult = 1 << 20
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "kb"):
		mult = 1 << 10
	}
	f, err := strconv.ParseFloat(strings.TrimRight(s, "gmkb"), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return uint64(f * mult)
}

func has(def map[string]any, key string) bool {
	_, ok := def[key]
	return ok
}

func hasPath(def map[string]any, keys ...string) bool {
	var cur any = def
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

// RenderLimitsOverlay renders the overlay YAML.
func RenderLimitsOverlay(limits []ServiceLimits) []byte {
	var b strings.Builder
	b.WriteString("# Managed by qabas: memory/process caps and dropped privileges (qabas limits show).\n")
	b.WriteString("# Disable with QABAS_LIMITS=off; override memory with QABAS_LIMIT_<SERVICE>=4g|none.\n")
	if len(limits) == 0 {
		b.WriteString("services: {}\n")
		return []byte(b.String())
	}
	b.WriteString("services:\n")
	for _, l := range limits {
		fmt.Fprintf(&b, "  %s:\n", l.Service)
		if l.MemoryBytes > 0 {
			fmt.Fprintf(&b, "    mem_limit: %dm\n", l.MemoryBytes/mib)
		}
		if l.PidsLimit > 0 {
			fmt.Fprintf(&b, "    pids_limit: %d\n", l.PidsLimit)
		}
		if l.NoNewPrivs {
			b.WriteString("    security_opt:\n      - no-new-privileges:true\n")
		}
		if l.Nofile > 0 {
			fmt.Fprintf(&b, "    ulimits:\n      nofile:\n        soft: %d\n        hard: %d\n", l.Nofile, l.Nofile)
		}
		if len(l.CapDrop) > 0 {
			b.WriteString("    cap_drop:\n")
			for _, c := range l.CapDrop {
				fmt.Fprintf(&b, "      - %s\n", c)
			}
		}
	}
	return []byte(b.String())
}

func LimitsOverlayPath(p paths.Paths) string { return filepath.Join(p.ComposeDir(), LimitsOverlay) }

func LimitsOverlayEnabled(p paths.Paths) bool {
	_, err := os.Stat(LimitsOverlayPath(p))
	return err == nil
}

func DisableLimitsOverlay(p paths.Paths) error {
	err := os.Remove(LimitsOverlayPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// managedOverlays are CLI-generated files excluded when reading upstream definitions.
var managedOverlays = map[string]bool{LoggingOverlay: true, LimitsOverlay: true}

func upstreamOverlays(p paths.Paths) []string {
	var out []string
	for _, f := range nodestack.ReadDirectives(p.ComposeDir()).Files {
		if !managedOverlays[filepath.Base(f)] {
			out = append(out, f)
		}
	}
	return out
}

// ServiceDefs merges service definitions across files (later files win per key).
func ServiceDefs(files []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var doc struct {
			Services map[string]map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(f), err)
		}
		for name, def := range doc.Services {
			if out[name] == nil {
				out[name] = map[string]any{}
			}
			for k, v := range def {
				out[name][k] = v
			}
		}
	}
	return out, nil
}

// commonDefs returns definitions of services present in every existing main
// compose file (each merged with this node's upstream overlays).
func commonDefs(p paths.Paths, composeFiles ...string) (map[string]map[string]any, error) {
	var common map[string]map[string]any
	for _, main := range composeFiles {
		if _, err := os.Stat(main); err != nil {
			continue
		}
		defs, err := ServiceDefs(append([]string{main}, upstreamOverlays(p)...))
		if err != nil {
			return nil, err
		}
		if common == nil {
			common = defs
			continue
		}
		for name, def := range common {
			other, ok := defs[name]
			if !ok {
				delete(common, name)
				continue
			}
			for k, v := range other {
				def[k] = v
			}
		}
	}
	if common == nil {
		return nil, errors.New("no compose bundle to derive limits overlay from")
	}
	return common, nil
}

// LiveServiceDefs are the live bundle's service definitions merged with this
// node's upstream overlays (managed overlays excluded).
func LiveServiceDefs(p paths.Paths) (map[string]map[string]any, error) {
	return commonDefs(p, p.ComposeFile())
}

// SyncLimitsOverlay (re)writes the limits overlay for services shared by the
// given compose files. Keys the release already sets are never overridden.
func SyncLimitsOverlay(p paths.Paths, ramBytes uint64, composeFiles ...string) (bool, []ServiceLimits, error) {
	if LimitsDisabled() {
		return false, nil, DisableLimitsOverlay(p)
	}
	defs, err := commonDefs(p, composeFiles...)
	if err != nil {
		return false, nil, err
	}
	limits := PlanLimits(defs, ramBytes)
	body := RenderLimitsOverlay(limits)
	if old, err := os.ReadFile(LimitsOverlayPath(p)); err == nil && bytes.Equal(old, body) {
		return false, limits, nodestack.AddDirective(p.ComposeDir(), "file:"+LimitsOverlay)
	}
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		return false, limits, err
	}
	if err := os.WriteFile(LimitsOverlayPath(p), body, 0o644); err != nil {
		return false, limits, err
	}
	return true, limits, nodestack.AddDirective(p.ComposeDir(), "file:"+LimitsOverlay)
}

// ReadLimitsOverlay parses the current overlay for display.
func ReadLimitsOverlay(p paths.Paths) (map[string]map[string]any, error) {
	return ServiceDefs([]string{LimitsOverlayPath(p)})
}
