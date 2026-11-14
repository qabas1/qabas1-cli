package cli

import (
	"fmt"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/output"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/secrets"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/spf13/cobra"
)

func newRuntime() *compose.Runtime { return compose.NewRuntime() }

// requireBackupSupported blocks vpg-backed commands (create, verify, restore)
// on hosts without vpg support. List, show, cp, and delete stay available.
func requireBackupSupported() error { return backup.RequireSupported() }

func newBackupCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage QabasOne backups",
		Long: `Create, list, verify, show, copy, delete, and restore QabasOne backups.

PostgreSQL is backed up with the host vpg tool (full cluster). Object storage
uses a MinIO volume archive (works even when containers are stopped or removed).
On releases declaring jsreport, S3-scope backups also include the Studio volume.
vpg must be installed on PATH; qabas does not install it.

Restore and verify also accept external vpg outputs (directory or .tar.gz) that
were created outside QabasOne.

` + backup.ScopeHelp,
		Example: `  # Create a full backup
  sudo qabas backup create

  # List backups
  qabas backup ls

  # Copy a backup to another path
  qabas backup cp qb-20260915-120000 /mnt/offsite/

  # Delete backups by name regex
  sudo qabas backup delete --name '^pre-deploy$' -y

  # Restore via nested command
  sudo qabas backup restore <backup-id>`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(
		newBackupCreateCmd(f),
		newBackupListCmd(f),
		newBackupVerifyCmd(f),
		newBackupShowCmd(f),
		newBackupCpCmd(f),
		newBackupDeleteCmd(f),
		newRestoreCmd(f),
		newBackupPITRCmd(f),
		newBackupScheduleCmd(f),
	)
	return cmd
}

func newBackupCreateCmd(f *flags) *cobra.Command {
	var db, s3 bool
	var name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a backup of persistent QabasOne state",
		Long: `Create a backup of PostgreSQL and/or object storage.

PostgreSQL uses vpg for a full-cluster logical backup (requires vpg on PATH).
When neither --db nor --s3 is set, both are included.
When --name is omitted, a label like manual_YYYYMMDD-HHMMSS is stored automatically
(the backup id stays qb-…). Use --name to override with a custom label.

` + backup.ScopeHelp,
		Example: `  sudo qabas backup create
  sudo qabas backup create --name "backup v1"
  sudo qabas backup create --db
  sudo qabas backup create --s3 --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBackupSupported(); err != nil {
				return err
			}
			e := load(f)
			return e.withLock(func() error {
				st, err := state.Load(e.Paths)
				if err != nil {
					return err
				}
				if !st.Installed() && !f.DryRun {
					return exitcode.NewHint(exitcode.InvalidInput,
						"sudo qabas install",
						"QabasOne is not installed")
				}
				includeDB, includeS3 := db, s3
				if !includeDB && !includeS3 {
					includeDB, includeS3 = true, true
				}
				if includeDB && !f.DryRun {
					if err := backup.RequireVPG(); err != nil {
						return err
					}
				}
				meta, err := backup.Create(e.Paths, newRuntime(), st, nil, db, s3, name, f.DryRun)
				if err != nil {
					return err
				}
				if f.JSON {
					return e.Out.Encode(meta)
				}
				if meta.Name != "" {
					e.Out.Printf("Backup %s (%s) created.\n", meta.ID, meta.Name)
				} else {
					e.Out.Printf("Backup %s created.\n", meta.ID)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&db, "db", false, "include PostgreSQL only (default: all when neither flag is set)")
	cmd.Flags().BoolVar(&s3, "s3", false, "include object storage only")
	cmd.Flags().StringVar(&name, "name", "", "human-readable label (default: manual_YYYYMMDD-HHMMSS)")
	return cmd
}

func newBackupListCmd(f *flags) *cobra.Command {
	var format string
	var namePatterns []string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List local backups",
		Example: `  qabas backup list
  qabas backup ls --json
  qabas backup ls --name 'pre-deploy'
  qabas backup ls --name '^alqabasy_.*'
  qabas backup ls --format '{{.ID}}'
  qabas backup ls --format 'table {{.ID}}\t{{.Name}}\t{{.Release}}\t{{.Created}}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			items, err := backup.List(e.Paths)
			if err != nil {
				return err
			}
			items, err = backup.Filter(items, nil, namePatterns)
			if err != nil {
				return err
			}
			rows := make([]backupListRow, 0, len(items))
			for _, m := range items {
				rows = append(rows, backupListRow{
					ID:       m.ID,
					Name:     m.Name,
					Release:  m.QabasOneRelease,
					Created:  m.CreatedAt.Format("2006-01-02 15:04"),
					DB:       m.Database.Included,
					S3:       m.S3.Included,
					Verified: m.Verified,
				})
			}
			if f.JSON {
				return e.Out.Encode(items)
			}
			return output.FormatList(cmd.OutOrStdout(), format,
				"table {{.ID}}\t{{.Name}}\t{{.Release}}\t{{.Created}}\t{{.DB}}\t{{.S3}}\t{{.Verified}}", rows)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", `Format output using a Go template ("table", "json", or a template)`)
	cmd.Flags().StringArrayVar(&namePatterns, "name", nil, "list backups whose name matches this Go regex (repeatable)")
	return cmd
}

type backupListRow struct {
	ID       string
	Name     string
	Release  string
	Created  string
	DB       bool
	S3       bool
	Verified bool
}

// formatBackupRef renders "id" or "id  (name)" for human delete/list lines.
func formatBackupRef(m backup.Meta) string {
	if m.Name == "" {
		return m.ID
	}
	return fmt.Sprintf("%s  (%s)", m.ID, m.Name)
}

func newBackupCpCmd(f *flags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "cp <name|id|regex> <dest>",
		Aliases: []string{"copy"},
		Short:   "Copy a managed backup to a destination path",
		Long: `Copy one managed QabasOne backup wrapper directory to a destination.

Selector resolution (exactly one match required):
  1. Exact backup id
  2. Exact backup name
  3. Go RE2 regex against id or name

Destination layout (like cp -R):
  • If dest exists and is a directory → copy to dest/<backup-id>/
  • If dest does not exist → create the wrapper directory at that path
    (parent directory must already exist)

Refuses to overwrite an existing target unless --force.
Does not remove or modify the source backup.`,
		Example: `  # By id into an existing directory
  qabas backup cp qb-20260915-120000 /mnt/offsite/

  # By exact name to a new path
  qabas backup cp pre-deploy /tmp/pre-deploy-copy

  # By regex (must match exactly one)
  qabas backup cp '^qb-20260915-' /mnt/offsite/

  # Preview
  qabas backup cp qb-20260915-120000 /mnt/offsite/ --dry-run`,
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return completeBackupIDs(cmd, args, toComplete)
			case 1:
				return nil, cobra.ShellCompDirectiveDefault
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			items, err := backup.List(e.Paths)
			if err != nil {
				return err
			}
			m, err := backup.ResolveOne(items, args[0])
			if err != nil {
				return err
			}
			src := backup.Dir(e.Paths, m.ID)
			finalDest, err := backup.Copy(src, args[1], force, f.DryRun)
			if err != nil {
				return err
			}
			result := map[string]string{
				"id":     m.ID,
				"name":   m.Name,
				"source": src,
				"dest":   finalDest,
			}
			if f.JSON {
				return e.Out.Encode(result)
			}
			if f.DryRun {
				e.Out.Printf("Would copy %s → %s\n", formatBackupRef(m), finalDest)
				return nil
			}
			e.Out.Printf("Copied %s → %s\n", formatBackupRef(m), finalDest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace destination if it already exists")
	return cmd
}

func newBackupDeleteCmd(f *flags) *cobra.Command {
	var (
		idPatterns   []string
		namePatterns []string
		except       string
		exceptName   string
		all          bool
	)
	cmd := &cobra.Command{
		Use:     "delete [ID...]",
		Aliases: []string{"rm"},
		Short:   "Delete local managed backups",
		Long: `Delete one or more managed QabasOne backups from the local backups directory.

Selection (at least one required):
  • positional ID…     exact backup ids
  • --id PATTERN       Go RE2 regex against backup id (repeatable; OR)
  • --name PATTERN     Go RE2 regex against backup name (repeatable; OR)
  • -v/--except PAT    keep backups whose id matches PAT; delete the rest
  • --except-name PAT  keep backups whose name matches PAT; delete the rest
  • --all              select every managed backup

Include selectors are unioned, then --except / --except-name remove matches.
When only -v/--except or --except-name is set, selection starts from all backups.

Requires confirmation unless --yes/-y or --dry-run.`,
		Example: `  # Exact ids
  sudo qabas backup delete qb-20260914-010002 qb-20260913-211518 -y

  # Delete by name regex
  sudo qabas backup delete --name '^pre-deploy$' -y

  # Delete by id regex
  sudo qabas backup delete --id 'qb-20260904-.*' -y

  # Keep one id; delete everything else
  sudo qabas backup delete -v 'mahros' -y

  # Named pre-deploy except one id
  sudo qabas backup delete --name 'pre-deploy' -v 'qb-20260914-010002' -y

  # Preview
  qabas backup delete --all --dry-run`,
		ValidArgsFunction: completeBackupIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			items, err := backup.List(e.Paths)
			if err != nil {
				return err
			}
			selected, err := backup.Select(items, backup.SelectOptions{
				IDs:          args,
				IDPatterns:   idPatterns,
				NamePatterns: namePatterns,
				Except:       except,
				ExceptName:   exceptName,
				All:          all,
			})
			if err != nil {
				return err
			}

			if !f.DryRun {
				var b strings.Builder
				fmt.Fprintf(&b, "Delete %d backup(s)?\n", len(selected))
				for _, m := range selected {
					fmt.Fprintf(&b, "  %s\n", formatBackupRef(m))
				}
				if err := e.confirm(strings.TrimRight(b.String(), "\n")); err != nil {
					return err
				}
			}

			run := func() error {
				deleted := make([]backup.Meta, 0, len(selected))
				for _, m := range selected {
					if f.DryRun {
						if !f.JSON {
							e.Out.Printf("would delete %s\n", formatBackupRef(m))
						}
						deleted = append(deleted, m)
						continue
					}
					if err := backup.Delete(e.Paths, m.ID, false); err != nil {
						return err
					}
					if !f.JSON {
						e.Out.Printf("deleted %s\n", formatBackupRef(m))
					}
					deleted = append(deleted, m)
				}
				if f.JSON {
					return e.Out.Encode(deleted)
				}
				if f.DryRun {
					e.Out.Printf("Would delete %d backup(s).\n", len(deleted))
					return nil
				}
				e.Out.Printf("Deleted %d backup(s).\n", len(deleted))
				return nil
			}
			if f.DryRun {
				return run()
			}
			return e.withLock(run)
		},
	}
	cmd.Flags().StringArrayVar(&idPatterns, "id", nil, "delete backups whose id matches this Go regex (repeatable)")
	cmd.Flags().StringArrayVar(&namePatterns, "name", nil, "delete backups whose name matches this Go regex (repeatable)")
	cmd.Flags().StringVarP(&except, "except", "v", "", "keep backups whose id matches this Go regex; delete the rest")
	cmd.Flags().StringVar(&exceptName, "except-name", "", "keep backups whose name matches this Go regex; delete the rest")
	cmd.Flags().BoolVar(&all, "all", false, "select every managed backup")
	return cmd
}

func newBackupVerifyCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <backup-id|path>",
		Short: "Verify a Qabas backup or an external vpg directory/archive",
		Long: `Verify backup integrity.

Accepts a Qabas backup id, a Qabas wrapper directory (meta.yaml), a vpg
backup directory, a .tar.gz archive, or a parent directory that contains a
complete vpg artifact.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBackupIDsOrFiles,
		Example: `  qabas backup verify <backup-id>
  qabas backup verify /tmp/qb-…/
  qabas backup verify /tmp/backup/postgres_cluster_….tar.gz
  qabas backup verify /tmp/backup/postgres_cluster_…
  qabas backup verify /tmp/backup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBackupSupported(); err != nil {
				return err
			}
			e := load(f)
			src, err := backup.ResolveSource(e.Paths, args[0])
			if err != nil {
				return err
			}
			if err := backup.VerifySource(e.Paths, src, nil); err != nil {
				return err
			}
			e.Out.Printf("Backup %s is valid.\n", src.Display)
			return nil
		},
	}
	return cmd
}

func newBackupShowCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "show <id>",
		Short:             "Show backup metadata",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBackupIDs,
		Example:           `  qabas backup show <backup-id>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			m, err := backup.Load(e.Paths, args[0])
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(m)
			}
			engine := m.Database.Engine
			if engine == "" && m.Database.Included {
				engine = "legacy"
			}
			if m.Name != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  name=%q  release=%s  db=%v  engine=%s  s3=%v  verified=%v\n",
					m.ID, m.Name, m.QabasOneRelease, m.Database.Included, engine, m.S3.Included, m.Verified)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  release=%s  db=%v  engine=%s  s3=%v  verified=%v\n",
					m.ID, m.QabasOneRelease, m.Database.Included, engine, m.S3.Included, m.Verified)
			}
			return nil
		},
	}
	return cmd
}

func newRestoreCmd(f *flags) *cobra.Command {
	var db, s3, dbOnly, s3Only, reconcileDB bool
	cmd := &cobra.Command{
		Use:   "restore <backup-id|path>",
		Short: "Restore QabasOne from a backup",
		Long: `Restore PostgreSQL and/or object storage from a local backup.

Accepts:
  - a Qabas backup id (qb-…) under the managed backups directory
  - a Qabas wrapper directory (contains meta.yaml; restores DB and/or S3)
  - a vpg backup directory (contains BACKUP_COMPLETE)
  - a vpg .tar.gz archive
  - a parent directory that contains a complete vpg artifact

Qabas wrappers (id or filesystem path with meta.yaml) restore PostgreSQL,
MinIO, and jsreport Studio data when present according to flags and metadata.
External vpg paths restore PostgreSQL only (no MinIO or jsreport data).
PostgreSQL restore uses vpg and replaces the FULL cluster (all databases and roles).
This is destructive disaster recovery — not a single-table restore.
Legacy single-database postgres.dump backups still use pg_restore.
When neither --db nor --s3 is set, both are restored from Qabas wrappers.
A safety backup is taken before restore (unless --dry-run).
After a database restore, PostgreSQL role passwords are matched to this host's
existing secrets and services are restarted. Use --reconcile-db-credentials=false
only when managing database credentials separately. Application user accounts
and passwords come from the restored database.

` + backup.ScopeHelp,
		Example: `  sudo qabas restore <backup-id>
  sudo qabas backup restore <backup-id> --db
  sudo qabas restore /tmp/qb-…/ --yes
  sudo qabas restore /tmp/backup/postgres_cluster_….tar.gz --yes
  sudo qabas restore /tmp/backup/postgres_cluster_… --yes
  sudo qabas restore /tmp/backup --yes`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBackupIDsOrFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBackupSupported(); err != nil {
				return err
			}
			e := load(f)
			src, err := backup.ResolveSource(e.Paths, args[0])
			if err != nil {
				return err
			}
			useDB := db || dbOnly
			useS3 := s3 || s3Only
			if src.Kind == backup.SourceVPG {
				if useS3 {
					return exitcode.New(exitcode.InvalidInput,
						"external vpg backups contain PostgreSQL only; omit --s3")
				}
				useDB = true
				useS3 = false
			}
			warnDB, warnS3 := useDB, useS3
			if src.Kind == backup.SourceQabas && !useDB && !useS3 {
				warnDB, warnS3 = true, true
			}
			if !f.DryRun {
				msg := backup.RestoreWarning(warnDB, warnS3) +
					fmt.Sprintf("\n\nRestore from %s and replace current data?", src.Display)
				if err := e.confirm(msg); err != nil {
					return err
				}
			}
			return e.withLock(func() error {
				st, err := state.Load(e.Paths)
				if err != nil {
					return err
				}
				st.Maintenance = true
				rt := newRuntime()
				imageEnv := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
				if !f.DryRun {
					if err := state.Save(e.Paths, st); err != nil {
						return err
					}
					if err := backup.RequireVPG(); err != nil {
						return err
					}
					safety, err := backup.Create(e.Paths, rt, st, nil, true, true, "pre-restore", false)
					if err != nil {
						return err
					}
					e.Out.Printf("Pre-restore safety backup: %s\n", safety.ID)
				}
				if err := backup.RestoreSource(e.Paths, rt, nil, nil, src, imageEnv, useDB, useS3, f.DryRun, true); err != nil {
					return err
				}
				if !f.DryRun && reconcileDB && !useS3 && (src.Kind == backup.SourceVPG || src.Meta != nil && src.Meta.Database.Included) {
					if err := reconcileRestoredDatabase(e.Paths, rt, imageEnv); err != nil {
						return fmt.Errorf("restore completed but database credential reconciliation failed (maintenance remains on): %w", err)
					}
				}
				st.Maintenance = false
				if !f.DryRun {
					if err := state.Save(e.Paths, st); err != nil {
						return err
					}
				}
				e.Out.Printf("Restore of %s complete.\n", src.Display)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&db, "db", false, "restore PostgreSQL only")
	cmd.Flags().BoolVar(&s3, "s3", false, "restore object storage only (Qabas backups only)")
	cmd.Flags().BoolVar(&dbOnly, "db-only", false, "restore PostgreSQL only")
	cmd.Flags().BoolVar(&s3Only, "s3-only", false, "restore object storage only")
	cmd.Flags().BoolVar(&reconcileDB, "reconcile-db-credentials", true, "match restored PostgreSQL role passwords to this host's existing secrets and restart services")
	_ = cmd.Flags().MarkDeprecated("db-only", "use --db")
	_ = cmd.Flags().MarkDeprecated("s3-only", "use --s3")
	return cmd
}

func reconcileRestoredDatabase(p paths.Paths, rt *compose.Runtime, env []string) error {
	b, err := secrets.ReadInstalled(p)
	if err != nil {
		return err
	}
	// The restored cluster contains the source host's role password hashes. Local
	// service env files remain in place, so reset the roles to those local values.
	sql := "ALTER ROLE CURRENT_USER WITH PASSWORD " + pgctl.Literal(b.PostgresPassword) + ";\n" +
		"ALTER ROLE qabasone_app WITH PASSWORD " + pgctl.Literal(b.PostgresAppPassword) + ";\n"
	if err := rt.ExecFromFile(p, env, "postgres", strings.NewReader(sql),
		"sh", "-c", `exec psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"`); err != nil {
		return err
	}
	return rt.Restart(p, env, "")
}
