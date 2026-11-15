package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/spf13/cobra"
)

func newDBCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Run database operations",
		Long:  "Run QabasOne database operations through the installed Docker Compose project.",
		Example: `  sudo qabas db migrate status
  sudo qabas db migrate
  sudo qabas db seed
  sudo qabas db seed --yes --command 'node dist/core/seed/seed.cli.js --only=bootstrap'`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newDBMigrateCmd(f), newDBSeedCmd(f))
	return cmd
}

func newDBMigrateCmd(f *flags) *cobra.Command {
	var (
		resolveApplied    string
		resolveRolledBack string
		noHeal            bool
	)
	cmd := &cobra.Command{
		Use:     "migrate",
		Aliases: []string{"migration", "migrations"},
		Short:   "Run database migrations",
		Long: `Run prisma migrate deploy through the installed Compose migrate service.
Use ` + "`sudo qabas db migrate status`" + ` to inspect applied, failed, rolled-back, and pending migrations without changing the database.

On P3009/P3018 failures caused by objects that already exist (e.g. duplicate
CREATE INDEX), qabas automatically marks the failed migration applied and
retries once. Use --no-heal to disable that behavior.

Manual recovery:
  sudo qabas db migrate --resolve-applied <migration_name>
  sudo qabas db migrate --resolve-rolled-back <migration_name>`,
		Example: `  sudo qabas db migrate status
  sudo qabas db migrate
  sudo qabas db migrate --resolve-applied 20260912040000_workday_open_per_party_unique`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resolveApplied != "" && resolveRolledBack != "" {
				return exitcode.New(exitcode.InvalidInput, "use only one of --resolve-applied or --resolve-rolled-back")
			}
			if resolveApplied != "" {
				return runDBMigrateResolve(f, resolveApplied, true)
			}
			if resolveRolledBack != "" {
				return runDBMigrateResolve(f, resolveRolledBack, false)
			}
			return runDBMigrate(f, !noHeal)
		},
	}
	cmd.Flags().StringVar(&resolveApplied, "resolve-applied", "", "mark a failed Prisma migration as applied without re-running SQL")
	cmd.Flags().StringVar(&resolveRolledBack, "resolve-rolled-back", "", "mark a failed Prisma migration as rolled back")
	cmd.Flags().BoolVar(&noHeal, "no-heal", false, "do not auto-resolve already-exists failed migrations")
	cmd.AddCommand(newDBMigrateStatusCmd(f))
	return cmd
}

func newDBMigrateStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List database migrations and their status",
		Long:  "Compare Prisma migration history in PostgreSQL with migration files in the installed release's migrate image. This command does not apply or resolve migrations.",
		Args:  cobra.NoArgs,
		Example: `  sudo qabas db migrate status
  sudo qabas db migrate status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := state.Load(e.Paths)
			if err != nil {
				return err
			}
			if !st.Installed() {
				return exitcode.NewHint(exitcode.InvalidInput,
					"run `sudo qabas install` first",
					"QabasOne is not installed")
			}
			env, err := compose.EnvForInstalledReleaseChecked(e.Paths, st.CurrentRelease)
			if err != nil {
				return err
			}
			rt := newRuntime()
			attempts, err := rt.MigrationHistory(e.Paths, env)
			if err != nil {
				return err
			}
			imageNames, err := rt.ImageMigrations(e.Paths, env)
			if err != nil {
				return err
			}
			report := compose.BuildMigrationStatus(st.CurrentRelease, attempts, imageNames)
			return printMigrationStatus(cmd, f, report)
		},
	}
}

func printMigrationStatus(cmd *cobra.Command, f *flags, report compose.MigrationStatusReport) error {
	w := cmd.OutOrStdout()
	if f.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	if f.Quiet {
		return nil
	}
	if _, err := fmt.Fprintf(w, "QabasOne %s migrations: %d applied, %d failed, %d rolled back, %d pending\n\n",
		report.Release, report.Counts.Applied, report.Counts.Failed, report.Counts.RolledBack, report.Counts.Pending); err != nil {
		return err
	}
	rows := make([][]string, 0, len(report.Migrations))
	for _, migration := range report.Migrations {
		last := migration.LastAttempt
		if last == "" {
			last = "-"
		}
		inImage := "no"
		if migration.InImage {
			inImage = "yes"
		}
		rows = append(rows, []string{migration.Name, migration.Status, fmt.Sprint(migration.Attempts), last, inImage})
	}
	return output.Table(w, []string{"MIGRATION", "STATUS", "ATTEMPTS", "LAST ATTEMPT (UTC)", "IN IMAGE"}, rows)
}

func newDBSeedCmd(f *flags) *cobra.Command {
	var command string
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed the database",
		Long: `Seed the QabasOne database by running a seed command inside a writable one-off migration container. Allowed only on the dev channel.

Interactive by default (confirm y/N in the container). Pass --yes for non-interactive approval on non-production targets.

When using --command, --yes still exports SEED_CONFIRM=yes into the container shell so the seed CLI can approve without a TTY.`,
		Example: `  sudo qabas db seed
  sudo qabas db seed --yes
  sudo qabas db seed --yes --command 'node dist/core/seed/seed.cli.js --only=bootstrap'
  sudo qabas db seed --command 'node_modules/.bin/tsx prisma/seed.ts'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			autoConfirm := f.Yes || f.NonInteractive
			if command == "" {
				command = defaultSeedCommand(autoConfirm)
			} else {
				command = withSeedAutoConfirm(command, autoConfirm)
			}
			return runDBCommand(f, "seed", command)
		},
	}
	cmd.Flags().StringVar(&command, "command", "", "shell command to run inside the migration container")
	return cmd
}

// withSeedAutoConfirm prepends SEED_CONFIRM=yes when qabas --yes is set so custom
// --command invocations do not hang on the seed CLI confirmation gate.
func withSeedAutoConfirm(command string, autoConfirm bool) string {
	if !autoConfirm {
		return command
	}
	return `export SEED_CONFIRM="${SEED_CONFIRM:-yes}"
` + command
}

func defaultSeedCommand(autoConfirm bool) string {
	prefix := ""
	if autoConfirm {
		prefix = `export SEED_CONFIRM="${SEED_CONFIRM:-yes}"
`
	}
	return prefix + `if [ -z "${POSTGRES_URI:-}" ] && [ -z "${DATABASE_URL:-}" ]; then
  echo "seed: POSTGRES_URI or DATABASE_URL is required" >&2
  exit 1
fi

cat > prisma.config.mjs <<'EOF'
export default {
  schema: "prisma/schema.prisma",
  migrations: { path: "prisma/migrations" },
  datasource: {
    url: process.env.POSTGRES_URI ?? process.env.DATABASE_URL,
  },
};
EOF

run_ts() {
  file="$1"
  if [ -x node_modules/.bin/tsx ]; then
    node_modules/.bin/tsx "$file"
  elif [ -x node_modules/.bin/ts-node ]; then
    node_modules/.bin/ts-node "$file"
  else
    cat > /tmp/qabas-seed-loader.mjs <<'EOF'
import { access } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const exts = [".ts", ".mts", ".js", ".mjs"];

function candidates(base) {
  const urls = [];
  if (base.protocol === "file:" && base.pathname.includes("/src/")) {
    for (const path of [
      base.pathname.replace("/src/", "/dist/src/"),
      base.pathname.replace("/src/", "/dist/"),
    ]) {
      for (const ext of [".js", ".mjs"]) {
        urls.push(new URL("file://" + path + ext));
      }
    }
  }
  for (const ext of exts) {
    urls.push(new URL(base.href + ext));
  }
  return urls;
}

export async function resolve(specifier, context, defaultResolve) {
  if (specifier.startsWith(".") || specifier.startsWith("/")) {
    const parent = context.parentURL || new URL("./", import.meta.url).href;
    const base = specifier.startsWith("/") ? new URL("file://" + specifier) : new URL(specifier, parent);
    for (const candidate of candidates(base)) {
      try {
        await access(fileURLToPath(candidate));
        return defaultResolve(candidate.href, context, defaultResolve);
      } catch {}
    }
  }
  try {
    return await defaultResolve(specifier, context, defaultResolve);
  } catch (err) {
    throw err;
  }
}
EOF
    tmp="${file%.ts}.qabas-run.mts"
    cp "$file" "$tmp"
    trap 'rm -f "$tmp" /tmp/qabas-seed-loader.mjs' EXIT
    node --experimental-strip-types --experimental-transform-types --experimental-loader /tmp/qabas-seed-loader.mjs "$tmp"
  fi
}

if [ -f dist/core/seed/seed.cli.js ]; then
  node dist/core/seed/seed.cli.js
elif [ -f dist/core/seed/seed.cli.mjs ]; then
  node dist/core/seed/seed.cli.mjs
elif [ -f dist/prisma/seed.js ]; then
  node dist/prisma/seed.js
elif [ -f dist/prisma/seed.mjs ]; then
  node dist/prisma/seed.mjs
elif [ -f prisma/seed.js ]; then
  node prisma/seed.js
elif [ -f prisma/seed.mjs ]; then
  node prisma/seed.mjs
elif [ -f prisma/seed.ts ]; then
  run_ts prisma/seed.ts
elif [ -f dist/prisma/seed-bootstrap.js ]; then
  node dist/prisma/seed-bootstrap.js
elif [ -f dist/prisma/seed-bootstrap.mjs ]; then
  node dist/prisma/seed-bootstrap.mjs
elif [ -f prisma/seed-bootstrap.js ]; then
  node prisma/seed-bootstrap.js
elif [ -f prisma/seed-bootstrap.mjs ]; then
  node prisma/seed-bootstrap.mjs
elif [ -f prisma/seed-bootstrap.ts ]; then
  run_ts prisma/seed-bootstrap.ts
elif command -v npm >/dev/null 2>&1 && node -e 'p=require("./package.json"); process.exit(p.scripts && p.scripts.seed ? 0 : 1)' >/dev/null 2>&1; then
  npm run seed
elif command -v pnpm >/dev/null 2>&1 && node -e 'p=require("./package.json"); process.exit(p.scripts && p.scripts.seed ? 0 : 1)' >/dev/null 2>&1; then
  pnpm run seed
elif command -v yarn >/dev/null 2>&1 && node -e 'p=require("./package.json"); process.exit(p.scripts && p.scripts.seed ? 0 : 1)' >/dev/null 2>&1; then
  yarn seed
else
  echo "no seed entrypoint found; rerun with --command 'node path/to/seed.js'" >&2
  exit 1
fi`
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runDBMigrate(f *flags, heal bool) error {
	return withDBRuntime(f, "migrate", func(e *env, env []string, rt *compose.Runtime) error {
		if heal {
			return rt.MigrateWithHeal(e.Paths, env)
		}
		_, err := rt.RunMigrateDeploy(e.Paths, env)
		return err
	})
}

func runDBMigrateResolve(f *flags, name string, applied bool) error {
	action := "resolve-rolled-back"
	if applied {
		action = "resolve-applied"
	}
	return withDBRuntime(f, "migrate "+action, func(e *env, envVars []string, rt *compose.Runtime) error {
		if applied {
			return rt.RunMigrateResolveApplied(e.Paths, envVars, name)
		}
		return rt.RunMigrateResolveRolledBack(e.Paths, envVars, name)
	})
}

func runDBCommand(f *flags, name, command string) error {
	return withDBRuntime(f, name, func(e *env, envVars []string, rt *compose.Runtime) error {
		if name != "seed" {
			return exitcode.New(exitcode.InvalidInput, "unsupported db command %q", name)
		}
		cfg, _, err := e.cfgState()
		if err != nil {
			return err
		}
		if e.channel() != "dev" && cfg.Channel != "dev" {
			return exitcode.NewHint(exitcode.InvalidInput,
				"set dev channel first: sudo qabas config set channel dev",
				"db seed is only allowed in dev")
		}
		autoConfirm := f.Yes || f.NonInteractive
		interactive := !autoConfirm && stdinIsTTY()
		if !autoConfirm && !interactive {
			return exitcode.NewHint(exitcode.InvalidInput,
				"rerun with a TTY, or pass --yes for non-interactive seed (non-production targets only)",
				"db seed needs interactive confirmation or --yes")
		}
		// Compose hardcodes NODE_ENV=production on migrate; that makes seed
		// treat docker-internal postgres as production-like and refuse --yes.
		seedEnv := []string{"NODE_ENV=development"}
		if interactive {
			return rt.RunOneOffInteractive(e.Paths, envVars, "migrate", command, seedEnv...)
		}
		return rt.RunOneOff(e.Paths, envVars, "migrate", command, seedEnv...)
	})
}

func withDBRuntime(f *flags, label string, fn func(e *env, env []string, rt *compose.Runtime) error) error {
	e := load(f)
	return e.withLock(func() error {
		cfg, st, err := e.cfgState()
		if err != nil {
			return err
		}
		if !st.Installed() {
			return exitcode.NewHint(exitcode.InvalidInput,
				"run `sudo qabas install` first",
				"QabasOne is not installed")
		}
		if f.DryRun {
			e.Out.Printf("would run db %s\n", label)
			return nil
		}
		// Ensure secrets/env exist for first setup only; never mutate existing
		// credential files. Missing files are rendered from deploy-release templates.
		sec, err := secrets.Ensure(e.Paths)
		if err != nil {
			return err
		}
		client := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref)
		if err := secrets.WriteEnvFiles(e.Paths, sec, cfg.EffectiveDomains(), client.FetchTemplate); err != nil {
			return err
		}
		env, err := compose.EnvForInstalledReleaseChecked(e.Paths, st.CurrentRelease)
		if err != nil {
			return err
		}
		if err := fn(e, env, newRuntime()); err != nil {
			return err
		}
		e.Out.Printf("db %s complete.\n", label)
		return nil
	})
}
