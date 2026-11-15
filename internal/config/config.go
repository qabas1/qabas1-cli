package config

import (
	"fmt"
	"os"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Channel string  `yaml:"channel"`
	Domain  string  `yaml:"domain"`
	Domains Domains `yaml:"domains"`
	Backup  Backup  `yaml:"backup"`
	Deploy  Deploy  `yaml:"deploy"`
}

type Domains struct {
	API       string `yaml:"api"`
	App       string `yaml:"app"`
	Dash      string `yaml:"dash"`
	Siraj     string `yaml:"siraj"`
	S3        string `yaml:"s3"`
	S3Console string `yaml:"s3Console"`
	Trade     string `yaml:"trade,omitempty"`
}

type Backup struct {
	Destination string   `yaml:"destination"`
	Schedule    Schedule `yaml:"schedule"`
}

// Schedule configures `qabas backup schedule` (daily full backups on the writer).
type Schedule struct {
	Enabled     bool   `yaml:"enabled"`
	At          string `yaml:"at"`       // HH:MM in Timezone
	Timezone    string `yaml:"timezone"` // IANA zone; "" = DefaultTimezone
	KeepDaily   int    `yaml:"keepDaily"`
	KeepWeekly  int    `yaml:"keepWeekly"`
	KeepMonthly int    `yaml:"keepMonthly"`
	Offsite     string `yaml:"offsite"` // replica node receiving a copy; "" = none
}

// DefaultTimezone is the schedule zone when none is configured.
const DefaultTimezone = "Africa/Cairo"

func DefaultSchedule() Schedule {
	return Schedule{At: "01:30", Timezone: DefaultTimezone, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}
}

// Zone is the configured IANA zone name, or DefaultTimezone when unset.
func (s Schedule) Zone() string {
	if z := strings.TrimSpace(s.Timezone); z != "" {
		return z
	}
	return DefaultTimezone
}

// Location loads Zone().
func (s Schedule) Location() (*time.Location, error) {
	z := s.Zone()
	if strings.EqualFold(z, "local") {
		return nil, fmt.Errorf("timezone %q is not allowed; use an IANA name such as %s or UTC", z, DefaultTimezone)
	}
	loc, err := time.LoadLocation(z)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q (use an IANA name such as %s or UTC)", z, DefaultTimezone)
	}
	return loc, nil
}

// Validate checks the time, timezone and retention; at least one backup must be kept.
func (s Schedule) Validate() error {
	if _, err := time.Parse("15:04", s.At); err != nil || len(s.At) != 5 {
		return exitcode.New(exitcode.InvalidInput, "backup schedule time %q must be HH:MM (24h)", s.At)
	}
	if _, err := s.Location(); err != nil {
		return exitcode.Wrap(exitcode.InvalidInput, err)
	}
	if s.KeepDaily < 0 || s.KeepWeekly < 0 || s.KeepMonthly < 0 {
		return exitcode.New(exitcode.InvalidInput, "backup retention counts must not be negative")
	}
	if s.KeepDaily+s.KeepWeekly+s.KeepMonthly == 0 {
		return exitcode.New(exitcode.InvalidInput, "backup retention must keep at least one backup")
	}
	return nil
}

type Deploy struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

func Default() Config {
	return Config{
		Channel: "stable",
		Backup:  Backup{Destination: "local", Schedule: DefaultSchedule()},
		Deploy: Deploy{
			Repo: envOr("QABAS_DEPLOY_REPO", "qabas1/qabas1-deploy"),
			Ref:  envOr("QABAS_DEPLOY_REF", "main"),
		},
	}
}

func Load(p paths.Paths) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, exitcode.Wrap(exitcode.InvalidInput, err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, exitcode.New(exitcode.InvalidInput, "parse %s: %w", p.ConfigFile(), err)
	}
	if v := os.Getenv("QABAS_DEPLOY_REPO"); v != "" {
		cfg.Deploy.Repo = v
	}
	if v := os.Getenv("QABAS_DEPLOY_REF"); v != "" {
		cfg.Deploy.Ref = v
	}
	// Repair the old default persisted by earlier CLI versions. Explicit custom
	// repositories remain untouched.
	if cfg.Deploy.Repo == "qabasone/qabas1-deploy" {
		cfg.Deploy.Repo = "qabas1/qabas1-deploy"
	}
	return cfg, nil
}

func Save(p paths.Paths, cfg Config) error {
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(p.ConfigFile(), b, 0o644)
}

func (c Config) Get(key string) (string, error) {
	switch key {
	case "channel":
		return c.Channel, nil
	case "domain":
		return c.Domain, nil
	case "domains.api":
		return c.Domains.API, nil
	case "domains.app":
		return c.Domains.App, nil
	case "domains.dash":
		return c.Domains.Dash, nil
	case "domains.siraj":
		return c.Domains.Siraj, nil
	case "domains.s3":
		return c.Domains.S3, nil
	case "domains.s3Console":
		return c.Domains.S3Console, nil
	case "domains.trade":
		return c.Domains.Trade, nil
	case "backup.destination":
		return c.Backup.Destination, nil
	case "deploy.repo":
		return c.Deploy.Repo, nil
	case "deploy.ref":
		return c.Deploy.Ref, nil
	default:
		return "", exitcode.New(exitcode.InvalidInput, "unknown config key %q", key)
	}
}

func (c *Config) Set(key, value string) error {
	switch key {
	case "channel":
		c.Channel = value
	case "domain":
		c.Domain = value
	case "domains.api":
		c.Domains.API = value
	case "domains.app":
		c.Domains.App = value
	case "domains.dash":
		c.Domains.Dash = value
	case "domains.siraj":
		c.Domains.Siraj = value
	case "domains.s3":
		c.Domains.S3 = value
	case "domains.s3Console":
		c.Domains.S3Console = value
	case "domains.trade":
		c.Domains.Trade = value
	case "backup.destination":
		c.Backup.Destination = value
	case "deploy.repo":
		c.Deploy.Repo = value
	case "deploy.ref":
		c.Deploy.Ref = value
	default:
		return exitcode.New(exitcode.InvalidInput, "unknown config key %q", key)
	}
	return nil
}

func (c Config) Map() map[string]string {
	return map[string]string{
		"channel":            c.Channel,
		"domain":             c.Domain,
		"domains.api":        c.Domains.API,
		"domains.app":        c.Domains.App,
		"domains.dash":       c.Domains.Dash,
		"domains.siraj":      c.Domains.Siraj,
		"domains.s3":         c.Domains.S3,
		"domains.s3Console":  c.Domains.S3Console,
		"domains.trade":      c.Domains.Trade,
		"backup.destination": c.Backup.Destination,
		"deploy.repo":        c.Deploy.Repo,
		"deploy.ref":         c.Deploy.Ref,
	}
}

func (c Config) Validate() error {
	switch c.Channel {
	case "stable", "rc", "beta", "nightly", "dev", "":
	default:
		return exitcode.New(exitcode.InvalidInput, "invalid channel %q", c.Channel)
	}
	switch c.Backup.Destination {
	case "local", "s3", "":
	default:
		return exitcode.New(exitcode.InvalidInput, "invalid backup.destination %q", c.Backup.Destination)
	}
	if c.Backup.Schedule.Enabled {
		if err := c.Backup.Schedule.Validate(); err != nil {
			return err
		}
	}
	if c.Deploy.Repo == "" {
		return exitcode.New(exitcode.InvalidInput, "deploy.repo is required")
	}
	for key, value := range c.EffectiveDomains().Map() {
		if strings.ContainsAny(value, "/: ") {
			return exitcode.New(exitcode.InvalidInput, "%s must be a hostname, got %q", key, value)
		}
	}
	return nil
}

func (c Config) EffectiveDomains() Domains {
	d := c.Domains
	if c.Domain != "" {
		if d.API == "" {
			d.API = "api." + c.Domain
		}
		if d.App == "" {
			d.App = "app." + c.Domain
		}
		if d.Dash == "" {
			d.Dash = "dash." + c.Domain
		}
		if d.Siraj == "" {
			d.Siraj = "siraj." + c.Domain
		}
		if d.S3 == "" {
			d.S3 = "s3." + c.Domain
		}
		if d.S3Console == "" {
			d.S3Console = "s3c." + c.Domain
		}
		if d.Trade == "" {
			d.Trade = "trade." + c.Domain
		}
	}
	return d
}

func (d Domains) Complete() bool {
	return d.API != "" && d.App != "" && d.Dash != "" && d.Siraj != "" && d.S3 != "" && d.S3Console != ""
}

func (d Domains) Map() map[string]string {
	return map[string]string{
		"domains.api":       d.API,
		"domains.app":       d.App,
		"domains.dash":      d.Dash,
		"domains.siraj":     d.Siraj,
		"domains.s3":        d.S3,
		"domains.s3Console": d.S3Console,
		"domains.trade":     d.Trade,
	}
}

func RedactMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if sensitive(k) {
			out[k] = redact(v)
			continue
		}
		out[k] = v
	}
	return out
}

func RedactEnv(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		if sensitive(k) {
			fmt.Fprintf(&b, "%s=%s\n", k, redact(v))
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, n := range []string{"secret", "password", "token", "key", "jwt", "credential"} {
		if strings.Contains(k, n) {
			return true
		}
	}
	return false
}

func redact(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return "********"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
