package cli

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"github.com/spf13/cobra"
)

func newSecretsCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Inspect and rotate credentials",
		Long: `Inspect, rotate, and back up QabasOne credentials.

Secrets live in /etc/qabasone (mode 0600, root only). Values are never printed;
qabas masks them in its own log, history, and error output.`,
		Example: `  qabas secrets status
  sudo qabas secrets rotate redis
  sudo qabas secrets recovery-kit`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newSecretsStatusCmd(f), newSecretsRotateCmd(f))
	return cmd
}

type secretStatus struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	File        string    `json:"file,omitempty"`
	Present     bool      `json:"present"`
	Mode        string    `json:"mode,omitempty"`
	ModeOK      bool      `json:"modeOk"`
	Since       time.Time `json:"since,omitempty"`
	AgeDays     int       `json:"ageDays,omitempty"`
	Estimated   bool      `json:"estimated,omitempty"`
	Due         bool      `json:"rotationDue"`
	Rotate      string    `json:"rotate"`
}

// extraSecrets are credentials outside secrets.env (name, description, file, how to rotate).
func extraSecrets(p paths.Paths) [][4]string {
	out := [][4]string{
		{"logs-ui", "Dozzle/Grafana login", p.LogsUICredentialsFile(), "qabas secrets rotate logs-ui"},
	}
	if src, ok := release.GitHubTokenSource(); ok && fileExists(src) {
		out = append(out, [4]string{"github-token", "GitHub release access token", src, "qabas secrets rotate github-token"})
	}
	if src, ok := release.GHCRTokenSource(); ok && fileExists(src) {
		out = append(out, [4]string{"ghcr-token", "container registry token", src, "qabas secrets rotate ghcr-token"})
	}
	return out
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// collectSecretStatus reports age and file mode for every known secret,
// back-filling secrets.meta.json from file mtimes (best-effort).
func collectSecretStatus(p paths.Paths, now time.Time) []secretStatus {
	meta := secrets.LoadMeta(p)
	changed := false
	var out []secretStatus
	add := func(name, desc, file, rotate string) {
		if meta.Backfill(name, file) {
			changed = true
		}
		s := secretStatus{Name: name, Description: desc, File: file, Rotate: rotate, ModeOK: true}
		if st, err := os.Stat(file); err == nil {
			s.Present = true
			if runtime.GOOS != "windows" {
				s.Mode = fmt.Sprintf("%04o", st.Mode().Perm())
				s.ModeOK = st.Mode().Perm()&0o077 == 0
			}
		}
		if stamp, ok := meta[name]; ok && s.Present {
			s.Since = stamp.Since()
			s.Estimated = stamp.Backfilled && stamp.RotatedAt.IsZero()
			s.AgeDays = int(now.Sub(s.Since).Hours() / 24)
			s.Due = now.Sub(s.Since) > secrets.MaxAge
		}
		out = append(out, s)
	}
	for _, sec := range secrets.Catalog {
		rotate := "qabas secrets rotate " + sec.Name
		if sec.Destructive {
			rotate += " --force"
		}
		add(sec.Name, sec.Desc, p.SecretsFile(), rotate)
	}
	for _, x := range extraSecrets(p) {
		add(x[0], x[1], x[2], x[3])
	}
	if changed {
		_ = secrets.SaveMeta(p, meta)
	}
	return out
}

func newSecretsStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show secret age, file permissions, and rotation hints",
		Long: fmt.Sprintf(`Show every QabasOne credential with its age and file permissions (values are never shown).

Secrets older than %d days are flagged for rotation. Ages of secrets created before
secrets.meta.json existed are estimated from the file modification time (marked ~).`, int(secrets.MaxAge.Hours()/24)),
		Example: "  qabas secrets status\n  qabas secrets status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			items := collectSecretStatus(e.Paths, time.Now())
			if f.JSON {
				return e.Out.Encode(items)
			}
			rows := make([][]string, 0, len(items))
			for _, s := range items {
				age, note := "-", ""
				if !s.Present {
					note = "missing"
				} else if !s.Since.IsZero() {
					age = fmt.Sprintf("%dd", s.AgeDays)
					if s.Estimated {
						age = "~" + age
					}
				}
				if s.Present && !s.ModeOK {
					note = "permissions too open (fix: sudo chmod 600 " + s.File + ")"
				} else if s.Due {
					note = "rotation due: " + s.Rotate
				}
				rows = append(rows, []string{s.Name, age, orDash(s.Mode), s.Description, orDash(note)})
			}
			return output.Table(cmd.OutOrStdout(), []string{"SECRET", "AGE", "MODE", "PURPOSE", "NOTE"}, rows)
		},
	}
}
