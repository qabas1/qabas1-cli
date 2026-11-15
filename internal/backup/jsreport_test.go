package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/manifest"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/state"
)

func TestReleaseUsesJSReport(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	rel := &manifest.Release{Infra: manifest.Infra{Jsreport: "jsreport/jsreport:4.15.0"}}
	if !releaseUsesJSReport(p, "1.0.0", rel) {
		t.Fatal("jsreport release should include Studio data")
	}
	if releaseUsesJSReport(p, "1.0.0", nil) {
		t.Fatal("legacy release should not require a Studio volume")
	}
}

func TestRejectUnsafeJSReportArchivePath(t *testing.T) {
	root := t.TempDir()
	m := &Meta{ID: "qb-test", JSReport: Part{Included: true, File: "../escape.tar"}}
	if err := validateWrapperMeta(filepath.Join(root, "backup"), m); err == nil {
		t.Fatal("expected unsafe jsreport archive path to be rejected")
	}
}

func TestS3BackupIncludesJSReportStore(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	rel := &manifest.Release{Infra: manifest.Infra{Jsreport: "jsreport/jsreport:4.15.0"}}
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			if strings.HasPrefix(joined, "volume inspect") {
				if stdout != nil {
					stdout.WriteString(args[len(args)-1])
				}
				return nil
			}
			if len(args) > 1 && args[0] == "run" {
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "-v" && strings.HasSuffix(args[i+1], ":/out") {
						return os.WriteFile(filepath.Join(strings.TrimSuffix(args[i+1], ":/out"), "data.tar"), []byte("archive"), 0o600)
					}
				}
			}
			return os.ErrNotExist
		},
	}
	m, err := Create(p, rt, state.State{CurrentRelease: "1.0.0"}, rel, false, true, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !m.JSReport.Included || m.JSReport.File != "jsreport/data.tar" {
		t.Fatalf("jsreport part: %+v", m.JSReport)
	}
	if _, err := os.Stat(filepath.Join(Dir(p, m.ID), "jsreport", "data.tar")); err != nil {
		t.Fatal(err)
	}
}
