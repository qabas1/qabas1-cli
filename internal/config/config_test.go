package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestGetSetValidate(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Set("channel", "rc"); err != nil {
		t.Fatal(err)
	}
	v, err := cfg.Get("channel")
	if err != nil || v != "rc" {
		t.Fatalf("got %q %v", v, err)
	}
	if err := cfg.Set("nope", "x"); err == nil {
		t.Fatal("expected unknown key")
	}
	if err := cfg.Set("channel", "bogus"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid channel")
	}
}

func TestRedact(t *testing.T) {
	m := config.RedactMap(map[string]string{
		"channel":     "stable",
		"jwt.secret":  "super-secret",
		"deploy.repo": "qabas1/qabas1-deploy",
	})
	if m["jwt.secret"] != "********" {
		t.Fatalf("secret %q", m["jwt.secret"])
	}
	if m["channel"] != "stable" {
		t.Fatalf("channel %q", m["channel"])
	}
	env := config.RedactEnv("JWT_SECRET=abc\nDOMAIN=app.example.com\n")
	if !strings.Contains(env, "JWT_SECRET=********") {
		t.Fatalf("env %q", env)
	}
	if !strings.Contains(env, "DOMAIN=app.example.com") {
		t.Fatalf("domain lost: %q", env)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: dir}
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "qabas.one" {
		t.Fatalf("domain %s", got.Domain)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleDefaultsAndValidate(t *testing.T) {
	cfg := config.Default()
	s := cfg.Backup.Schedule
	if s.At != "01:30" || s.Timezone != "Africa/Cairo" || s.KeepDaily != 7 || s.KeepWeekly != 4 || s.KeepMonthly != 6 || s.Enabled {
		t.Fatalf("default schedule %#v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("default schedule invalid: %v", err)
	}
	legacy := config.Schedule{At: "01:30", KeepDaily: 1}
	if loc, err := legacy.Location(); err != nil || loc.String() != "Africa/Cairo" || legacy.Zone() != "Africa/Cairo" {
		t.Fatalf("empty timezone = %v, %v", loc, err)
	}
	if err := (config.Schedule{At: "01:30", Timezone: "UTC", KeepDaily: 1}).Validate(); err != nil {
		t.Fatalf("UTC rejected: %v", err)
	}
	for _, bad := range []config.Schedule{
		{At: "1:30", KeepDaily: 1},
		{At: "25:00", KeepDaily: 1},
		{At: "01:30"},
		{At: "01:30", KeepDaily: -1, KeepWeekly: 2},
		{At: "01:30", Timezone: "Mars/Olympus", KeepDaily: 1},
		{At: "01:30", Timezone: "Local", KeepDaily: 1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
}

func TestEffectiveDomainsFromBaseDomain(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "example.com"
	d := cfg.EffectiveDomains()
	if d.API != "api.example.com" || d.App != "app.example.com" || d.Dash != "dash.example.com" || d.Siraj != "siraj.example.com" || d.S3 != "s3.example.com" || d.S3Console != "s3c.example.com" {
		t.Fatalf("domains %#v", d)
	}
}

func TestEffectiveDomainsAllowOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "example.com"
	cfg.Domains.API = "backend.example.com"
	d := cfg.EffectiveDomains()
	if d.API != "backend.example.com" || d.App != "app.example.com" || d.Dash != "dash.example.com" || d.Siraj != "siraj.example.com" || d.S3 != "s3.example.com" || d.S3Console != "s3c.example.com" {
		t.Fatalf("domains %#v", d)
	}

}
