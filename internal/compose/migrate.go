package compose

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

// Shell that matches compose migrate service: materialize Prisma 7 config then run a prisma subcommand.
const prismaConfigShellPrefix = `if [ -z "${POSTGRES_URI:-}" ] && [ -z "${DATABASE_URL:-}" ]; then
  echo "migrate: POSTGRES_URI or DATABASE_URL is required" >&2
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
`

var (
	reFailedMigrationBacktick = regexp.MustCompile("The `([^`]+)` migration started at .+ failed")
	reMigrationNameLine       = regexp.MustCompile(`(?m)^Migration name:\s*(.+?)\s*$`)
	reAlreadyExists           = regexp.MustCompile(`(?i)already exists|42P07|42710`)
	reFailedMigrationBlock    = regexp.MustCompile(`(?i)P3009|failed migrations in the target database|P3018`)
	reMigrationNameOK         = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func validMigrationName(name string) bool {
	return reMigrationNameOK.MatchString(name)
}

// ParseFailedMigrationName extracts the Prisma migration name from deploy/resolve error text.
func ParseFailedMigrationName(output string) string {
	if m := reFailedMigrationBacktick.FindStringSubmatch(output); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	if m := reMigrationNameLine.FindStringSubmatch(output); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// IsPrismaFailedMigrationError reports P3009/P3018-class migrate failures.
func IsPrismaFailedMigrationError(output string) bool {
	return reFailedMigrationBlock.MatchString(output)
}

// IsAlreadyExistsMigrationFailure reports duplicate-object apply failures that are safe to mark applied.
func IsAlreadyExistsMigrationFailure(output string) bool {
	return reAlreadyExists.MatchString(output)
}

func prismaCLI(args string) string {
	return prismaConfigShellPrefix + "exec node node_modules/prisma/build/index.js " + args + "\n"
}

// RunMigrateDeploy runs compose migrate (prisma migrate deploy) and returns combined output.
func (r *Runtime) RunMigrateDeploy(p paths.Paths, env []string) (string, error) {
	return r.runMigrateServiceTee(p, env)
}

// RunMigrateResolveApplied marks a failed/pending migration as applied without re-running SQL.
func (r *Runtime) RunMigrateResolveApplied(p paths.Paths, env []string, name string) error {
	name = strings.TrimSpace(name)
	if !validMigrationName(name) {
		return exitcode.New(exitcode.InvalidInput, "invalid migration name %q", name)
	}
	_, err := r.RunOneOffOut(p, env, "migrate", prismaCLI("migrate resolve --applied "+name))
	return err
}

// RunMigrateResolveRolledBack marks a failed migration as rolled back so a fixed SQL can re-apply.
func (r *Runtime) RunMigrateResolveRolledBack(p paths.Paths, env []string, name string) error {
	name = strings.TrimSpace(name)
	if !validMigrationName(name) {
		return exitcode.New(exitcode.InvalidInput, "invalid migration name %q", name)
	}
	_, err := r.RunOneOffOut(p, env, "migrate", prismaCLI("migrate resolve --rolled-back "+name))
	return err
}

// MigrateWithHeal runs migrate deploy; on P3009/P3018 caused by "already exists", marks the
// failed migration applied and retries deploy once. Non-duplicate failures are returned as-is.
func (r *Runtime) MigrateWithHeal(p paths.Paths, env []string) error {
	out, err := r.RunMigrateDeploy(p, env)
	if err == nil {
		return nil
	}
	combined := out + "\n" + err.Error()
	if !IsPrismaFailedMigrationError(combined) {
		return err
	}
	name := ParseFailedMigrationName(combined)
	if name == "" {
		return exitcode.NewHint(exitcode.Health,
			"inspect migrate logs, then: sudo qabas db migrate --resolve-applied <migration_name>",
			"prisma migrate failed and the failed migration name could not be parsed")
	}

	reason := combined
	if !IsAlreadyExistsMigrationFailure(combined) {
		logs, logErr := r.fetchFailedMigrationLogs(p, env, name)
		if logErr == nil && logs != "" {
			reason = logs
		}
	}
	if !IsAlreadyExistsMigrationFailure(reason) {
		return exitcode.NewHint(exitcode.Health,
			fmt.Sprintf("if the migration DDL is already present: sudo qabas db migrate --resolve-applied %s", name),
			"prisma migrate blocked on failed migration %s", name)
	}

	fmt.Fprintf(os.Stderr, "migrate: healing failed migration %s (object already exists) via resolve --applied\n", name)
	if resolveErr := r.RunMigrateResolveApplied(p, env, name); resolveErr != nil {
		return exitcode.NewHint(exitcode.Health,
			fmt.Sprintf("retry manually: sudo qabas db migrate --resolve-applied %s", name),
			"failed to mark migration %s as applied: %v", name, resolveErr)
	}
	if _, retryErr := r.RunMigrateDeploy(p, env); retryErr != nil {
		return retryErr
	}
	return nil
}

func (r *Runtime) fetchFailedMigrationLogs(p paths.Paths, env []string, name string) (string, error) {
	if !validMigrationName(name) {
		return "", exitcode.New(exitcode.InvalidInput, "invalid migration name %q", name)
	}
	sql := `SELECT coalesce(logs, '') FROM "_prisma_migrations" WHERE migration_name = '` + name + `' AND finished_at IS NULL LIMIT 1;`
	out, err := r.Exec(p, env, "postgres", "psql", "-U", "qabasone", "-d", "qabasone", "-tAc", sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Runtime) runMigrateServiceTee(p paths.Paths, env []string) (string, error) {
	var buf bytes.Buffer
	out := io.MultiWriter(os.Stdout, &buf)
	errOut := io.MultiWriter(os.Stderr, &buf)
	args := []string{"run", "--rm", "--remove-orphans", "migrate"}
	err := r.dockerIO(p, env, args, nil, out, errOut)
	return buf.String(), err
}

// RunOneOffOut is RunOneOff that captures and returns combined stdout/stderr (also teed to the console).
func (r *Runtime) RunOneOffOut(p paths.Paths, env []string, service string, command string, runEnv ...string) (string, error) {
	args := []string{}
	overrideArgs, err := oneOffComposeOverrideArgs(p, service)
	if err != nil {
		return "", err
	}
	args = append(args, overrideArgs...)
	args = append(args, "run", "--rm", "--remove-orphans")
	for _, e := range runEnv {
		args = append(args, "-e", e)
	}
	args = append(args, "--entrypoint", "sh", service, "-ec", command)
	var buf bytes.Buffer
	out := io.MultiWriter(os.Stdout, &buf)
	errOut := io.MultiWriter(os.Stderr, &buf)
	runErr := r.dockerIO(p, env, args, nil, out, errOut)
	return buf.String(), runErr
}
