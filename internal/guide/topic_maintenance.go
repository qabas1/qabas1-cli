package guide

func maintenanceTopic() Topic {
	return Topic{
		ID:      "maintenance",
		Aliases: []string{"maint", "troubleshoot", "troubleshooting", "db", "logs", "doctor"},
		Title:   "Maintenance mode, database tasks, logs and troubleshooting",
		Summary: "Day-two operations: put the system in maintenance mode for planned work, run or " +
			"repair database migrations, read logs (CLI or a local web UI), and diagnose problems " +
			"with doctor, status and history.",
		Build: buildMaintenance,
	}
}

func buildMaintenance(f Facts, _ Vars) Doc {
	d := Doc{Intro: []string{
		"Short path when something is wrong: `qabas status` -> `qabas doctor --verbose` -> " +
			"`qabas logs <service>` -> `qabas history` -> fix -> `qabas doctor` again.",
	}}
	maint := Step{
		Text: "Enter maintenance mode (users see a maintenance page with the expected return time), do the work, leave it.",
		Commands: []string{
			"sudo qabas maintenance on --for 30m",
			"sudo qabas maintenance on --until '2026-10-01 18:00'",
			"sudo qabas maintenance on --page /etc/qabasone/maintenance.html",
			"sudo qabas maintenance off",
			"qabas maintenance status",
		},
	}
	if f.Detected && f.Maintenance {
		maint.Status = todo("maintenance mode is ON")
	}
	d.Sections = []Section{
		{Title: "Diagnose", Where: WhereHere, Steps: []Step{
			{Text: "Overall state, containers and checks (doctor exits non-zero on failures).",
				Commands: []string{"qabas status", "qabas ps", "qabas doctor", "qabas doctor --verbose", "qabas doctor --json"}},
			{Text: "What qabas did recently (deploys, backups, replica and DR actions) and whether it failed.",
				Commands: []string{"qabas history", "qabas history --format '{{.Time}} {{.Action}} {{.Status}}'"}},
			{Text: "Effective configuration (secrets redacted).",
				Commands: []string{"qabas config show", "qabas config get channel", "qabas config validate"}},
		}},
		{Title: "Machine resources and autopilot", Where: WhereHere, Intro: []string{
			"Autopilot is the hourly self-maintenance pass (enabled by install): it prunes dangling Docker " +
				"images and old build cache, removes unreplayable PITR WAL, applies backup retention early when " +
				"disk is low, rotates container and qabas logs, re-tightens file permissions, ends expired " +
				"maintenance windows and restarts unhealthy services.",
		}, Steps: []Step{
			{Text: "CPU, memory, swap, disk for QabasOne data, uptime and Docker usage, graded ok/warn/crit.",
				Commands: []string{"qabas sysinfo", "qabas sysinfo --watch", "qabas sysinfo --json"}},
			{Text: "Live dashboard of services, PostgreSQL activity, replica lag and findings (alias: htop).",
				Commands: []string{"qabas top", "qabas top --once", "qabas top --json"}},
			{Text: "Schedule, inspect or run autopilot.",
				Commands: []string{"sudo qabas autopilot enable", "qabas autopilot status", "sudo qabas autopilot run", "sudo qabas autopilot disable"}},
		}},
		{Title: "Logs", Where: WhereHere, Steps: []Step{
			{Text: "Container logs from the CLI.",
				Commands: []string{"qabas logs", "qabas logs api", "qabas logs -f api"}},
			{Text: "Local logs web UI (Dozzle); --full adds Loki + Grafana for searchable history.",
				Commands: []string{"qabas logs ui up", "qabas logs ui up --full", "qabas logs ui open", "qabas logs ui open --grafana", "qabas logs ui status", "qabas logs ui down"}},
			{Text: "How every log is bounded (qabas.log, history, container logs, Loki) and how big each is now.",
				Commands: []string{"qabas logs retention", "sudo qabas logs retention --apply", "qabas history --all"},
				Notes:    []string{"Loki keeps 7 days by default; set QABAS_LOGS_RETENTION=14d before qabas logs ui up --full."}},
		}},
		{Title: "Secrets, limits and security", Where: WhereHere, Intro: []string{
			"Doctor warns (never fails) about loose secret permissions, secrets older than 180 days, " +
				"unbounded logs, missing resource limits, ports published beyond 80/443, and Docker socket mounts.",
		}, Steps: []Step{
			{Text: "Credential age, file modes and purpose.",
				Commands: []string{"qabas secrets status"}},
			{Text: "Rotate credentials on the writer (backup, maintenance, apply, verify, automatic rollback).",
				Commands: []string{"sudo qabas secrets rotate all", "sudo qabas secrets rotate jwt", "sudo qabas secrets rotate logs-ui"},
				Notes:    []string{"2fa and pepper invalidate existing data and need --force."}},
			{Text: "Per-service memory/pids limits sized from host RAM (regenerated on every deploy).",
				Commands: []string{"qabas limits show", "sudo qabas limits sync"},
				Notes:    []string{"Override with QABAS_LIMIT_<SERVICE>=2g, or QABAS_LIMITS=off."}},
		}},
		{Title: "Maintenance mode", Where: writerWhere(f), Steps: []Step{maint}},
		{Title: "Services", Where: WhereHere, Steps: []Step{
			{Text: "Start, stop or restart the stack or one service (compose project qabasone).",
				Commands: []string{"sudo qabas restart api", "sudo qabas stop app", "sudo qabas start app", "sudo qabas up", "sudo qabas down"}},
		}},
		{Title: "Database", Where: writerWhere(f), Intro: []string{
			"Writer only. Replicas receive schema through WAL and never run migrations.",
		}, Steps: []Step{
			{Text: "List applied, failed, rolled-back and pending migrations for the installed release.",
				Commands: []string{"sudo qabas db migrate status", "sudo qabas db migrate status --json"}},
			{Text: "Run pending migrations (deploy/upgrade do this automatically).",
				Commands: []string{"sudo qabas db migrate"}},
			{Text: "A migration failed: fix the cause, then mark it applied (SQL already ran) or rolled back (so it re-runs).",
				Commands: []string{
					"sudo qabas db migrate --resolve-applied <migration_name>",
					"sudo qabas db migrate --resolve-rolled-back <migration_name>",
				},
				Notes: []string{"Already-exists failures are auto-resolved; --no-heal disables that."}},
			{Text: "Seed data (asks for confirmation), or run a custom command inside the migration container.",
				Commands: []string{
					"sudo qabas db seed",
					"sudo qabas db seed --yes --command 'node dist/core/seed/seed.cli.js --only=bootstrap'",
				}},
		}},
	}
	d.Troubleshooting = []QA{
		{Problem: "doctor reports a failed check", Fix: "The check detail names the service; read its logs and restart it.", Commands: []string{"qabas logs <service>", "sudo qabas restart <service>"}},
		{Problem: "backup-schedule warning in doctor", Fix: "See `qabas guide schedule`.", Commands: []string{"qabas backup schedule status"}},
		{Problem: "API is read-only on the writer", Fix: "The writer lost its lease (fenced) or maintenance is on.", Commands: []string{"qabas maintenance status", "sudo qabas dr lease show"}},
		{Problem: "failed Prisma migration blocks deploy", Fix: "Inspect the migration error in the deploy output, fix the data, then resolve it.", Commands: []string{"sudo qabas db migrate --resolve-rolled-back <migration_name>", "sudo qabas db migrate"}},
		{Problem: "disk full", Fix: "Run autopilot now (Docker cache, WAL, scheduled-backup retention), then prune old backups (preview first) and check WAL retention on replicas / PITR.", Commands: []string{"qabas sysinfo", "sudo qabas autopilot run", "qabas backup delete --except-name '^scheduled$' --dry-run", "sudo qabas backup pitr status"}},
		{Problem: "autopilot warning in doctor", Fix: "The detail names the failed task; inspect the last run and re-run it.", Commands: []string{"qabas autopilot status", "sudo qabas autopilot run"}},
		{Problem: "secret-perms or secret-age warning in doctor", Fix: "Autopilot re-tightens modes; rotate old secrets on the writer.", Commands: []string{"sudo qabas autopilot run", "qabas secrets status", "sudo qabas secrets rotate all"}},
		{Problem: "qabasone-alloy restarts with positions.yml: permission denied", Fix: "Its volume was created when Alloy ran as root; `logs ui up --full` re-owns it to uid 473 before starting. To repair by hand, run the chown and restart Alloy.", Commands: []string{"sudo qabas logs ui up --full", "docker run --rm --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --network none -v qabasone-logs_alloy_data:/d --entrypoint chown ghcr.io/qabasone/vendor/grafana/alloy:v1.7.5 -R 473:473 /d", "docker restart qabasone-alloy"}},
		{Problem: "exposure warning in doctor", Fix: "A container port is published on all interfaces; bind it to 127.0.0.1 in the release compose or firewall it."},
		{Problem: "permission denied / must run as root", Fix: "Commands that change the host need root on Linux; prefix them with sudo (e.g. sudo qabas deploy)."},
		{Problem: "error: ... hint: ...", Fix: "Every qabas error prints a hint with the next command to run; exit codes are listed in the README."},
	}
	d.SeeAlso = []string{"qabas guide install", "qabas guide backup", "qabas doctor --help"}
	return d
}
