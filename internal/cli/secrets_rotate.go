package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/observability"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/secrets"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/spf13/cobra"
)

const rotateMaintenanceWindow = 15 * time.Minute

func newSecretsRotateCmd(f *flags) *cobra.Command {
	var force, noBackup bool
	cmd := &cobra.Command{
		Use:   "rotate <name|all>...",
		Short: "Rotate credentials safely (backup, maintenance, apply, verify, rollback)",
		Long: `Rotate QabasOne credentials. For each run qabas:

  1. takes a safety backup (skip with --no-backup)
  2. turns on the maintenance page
  3. changes the live value (ALTER ROLE for postgres) and rewrites secrets.env
     and every service env file atomically
  4. recreates affected containers and waits until they are healthy
  5. turns maintenance off

If anything fails after step 3, the previous files and database password are
restored and the stack is re-applied.

Names: postgres, redis, minio, jwt (signs every user out), 2fa and pepper
(destructive: need --force), all (every non-destructive secret), and logs-ui.
GitHub/GHCR tokens are issued by GitHub; rotating them prints the steps.`,
		Example: `  sudo qabas secrets rotate redis
  sudo qabas secrets rotate all
  sudo qabas secrets rotate jwt --yes
  sudo qabas secrets rotate logs-ui`,
		Args:      cobra.MinimumNArgs(1),
		ValidArgs: rotateNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			var stack []string
			for _, a := range args {
				switch a {
				case "logs-ui":
					if err := rotateLogsUI(e, f); err != nil {
						return err
					}
				case "github-token", "ghcr-token":
					return exitcode.NewHint(exitcode.InvalidInput,
						"create a new fine-grained token on github.com, then: sudo qabas auth login --token-file <file>; revoke the old token afterwards",
						"%s is issued by GitHub and cannot be generated locally", a)
				default:
					stack = append(stack, a)
				}
			}
			if len(stack) == 0 {
				return nil
			}
			names, err := secrets.ResolveNames(stack, force)
			if err != nil {
				return exitcode.New(exitcode.InvalidInput, "%s", err)
			}
			return rotateStack(e, f, names, noBackup)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow destructive rotations (2fa, pepper)")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the safety backup")
	return cmd
}

func rotateNames() []string {
	out := []string{"all", "logs-ui", "github-token", "ghcr-token"}
	for _, s := range secrets.Catalog {
		out = append(out, s.Name)
	}
	return out
}

func rotateStack(e *env, f *flags, names []string, noBackup bool) error {
	var impacts []string
	for _, n := range names {
		s, _ := secrets.Lookup(n)
		impacts = append(impacts, fmt.Sprintf("  %-9s %s", s.Name, s.Impact))
	}
	e.Out.Printf("Rotating:\n%s\n", strings.Join(impacts, "\n"))
	if f.DryRun {
		e.Out.Println("would back up, enable maintenance, rewrite secrets.env and env files, recreate containers, verify")
		return nil
	}
	if c, err := cluster.Load(e.Paths); err == nil {
		if self := e.selfName(c); self != c.Writer {
			return exitcode.New(exitcode.InvalidInput, "rotate secrets on the writer (%s); replicas follow with qabas replica sync --all --restart", c.Writer)
		}
	}
	if err := e.confirm("The stack restarts during rotation. Continue?"); err != nil {
		return err
	}
	return e.withLock(func() error {
		st, err := state.Load(e.Paths)
		if err != nil {
			return err
		}
		if !st.Installed() {
			return exitcode.New(exitcode.InvalidInput, "QabasOne is not installed")
		}
		rt := newRuntime()
		env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
		d := secrets.RotateDeps{
			SetPostgresPassword: func(pw string) error {
				sql := "ALTER ROLE CURRENT_USER WITH PASSWORD " + pgctl.Literal(pw) + ";\n"
				return rt.ExecFromFile(e.Paths, env, "postgres", strings.NewReader(sql),
					"sh", "-c", `exec psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`)
			},
			SetPostgresAppPassword: func(pw string) error {
				sql := "ALTER ROLE qabasone_app WITH PASSWORD " + pgctl.Literal(pw) + ";\n"
				return rt.ExecFromFile(e.Paths, env, "postgres", strings.NewReader(sql),
					"sh", "-c", `exec psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`)
			},
			Apply: func() error { return rt.Up(e.Paths, env) },
			Verify: func() error {
				svcs, err := rt.ServiceStatus(e.Paths, env, st.CurrentRelease)
				if err != nil {
					return err
				}
				for _, s := range svcs {
					if strings.EqualFold(s.Health, "unhealthy") {
						return fmt.Errorf("%s is unhealthy", s.Service)
					}
				}
				return nil
			},
		}
		if !noBackup && backup.Supported() {
			d.Backup = func() (string, error) {
				meta, err := backup.Create(e.Paths, rt, st, nil, true, true, "pre-rotate", false)
				return meta.ID, err
			}
		}
		if !st.Maintenance {
			d.Maintenance = func(on bool) error {
				until := time.Time{}
				if on {
					until = time.Now().Add(rotateMaintenanceWindow).UTC()
				}
				return setMaintenanceMode(e.Paths, &st, on, until, "")
			}
		}
		res, err := secrets.Rotate(e.Paths, names, d)
		audit(e, "secrets-rotate", "", strings.Join(names, ","), err, "backup="+res.Backup)
		if err != nil {
			return wrapErr(err)
		}
		if f.JSON {
			return e.Out.Encode(res)
		}
		e.Out.Printf("Rotated %s (%d file(s) updated", strings.Join(res.Rotated, ", "), len(res.Files))
		if res.Backup != "" {
			e.Out.Printf(", safety backup %s", res.Backup)
		}
		e.Out.Println(").")
		if _, err := cluster.Load(e.Paths); err == nil {
			e.Out.Println("Cluster: push the new secrets to replicas with: sudo qabas replica sync --all --restart")
		}
		return nil
	})
}

func rotateLogsUI(e *env, f *flags) error {
	if f.DryRun {
		e.Out.Println("would generate new logs UI passwords and restart the logs UI if running")
		return nil
	}
	cred, running, err := observability.NewRuntime().RotateCredentials(e.Paths)
	audit(e, "secrets-rotate", "", "logs-ui", err, "")
	if err != nil {
		return wrapErr(err)
	}
	meta := secrets.LoadMeta(e.Paths)
	meta.Rotated("logs-ui", time.Now())
	_ = secrets.SaveMeta(e.Paths, meta)
	e.Out.Printf("New logs UI passwords written to %s (mode 0600).\n", cred.Path)
	if running {
		e.Out.Println("Logs UI restarted with the new passwords.")
	}
	return nil
}
