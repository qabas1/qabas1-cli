package guide

import (
	"fmt"

	"github.com/qabas1/qabasone-cli/internal/backup"
	"github.com/qabas1/qabasone-cli/internal/config"
)

func backupTopic() Topic {
	return Topic{
		ID:      "backup",
		Aliases: []string{"backups", "restore"},
		Title:   "Create, inspect, copy, prune and restore backups",
		Summary: "A QabasOne backup is a wrapper directory (qb-YYYYMMDD-HHMMSS) holding a full " +
			"PostgreSQL cluster dump made with the host `vpg` tool and/or a MinIO object-storage " +
			"archive, plus meta.yaml. vpg must be on PATH (qabas does not install it). " +
			"Restore replaces the WHOLE database cluster: it is disaster recovery, not an undo for " +
			"one record (use `qabas guide pitr` for that).",
		Build: buildBackup,
	}
}

func buildBackup(f Facts, v Vars) Doc {
	here := WhereHere
	if f.Detected && f.Role == RoleReplica {
		here = WhereWriter
	}
	d := Doc{Intro: []string{
		"Short path: `backup create` -> `backup verify` -> copy it off the machine with " +
			"`backup cp` -> restore with `qabas restore <id>` when needed. For automatic daily " +
			"backups see `qabas guide schedule`.",
	}}
	if f.Detected && f.Role == RoleReplica {
		d.Intro = append(d.Intro, "This machine is a replica: take backups on the writer ("+f.Writer+"). The scheduled job skips replicas.")
	}
	installed := Step{
		Text:     "Prerequisites: QabasOne installed and `vpg` on PATH.",
		Status:   check(f, f.Installed, "QabasOne is not installed"),
		Commands: []string{"qabas status", "vpg --version"},
	}
	create := Step{
		Text: "Create a full backup (database + object storage). Without --name a label " +
			"manual_YYYYMMDD-HHMMSS is stored.",
		Status: progress(f, f.Backups > 0, fmt.Sprintf("%d backup(s), newest %s", f.Backups, f.LatestBackup)),
		Commands: []string{
			"sudo qabas backup create",
			`sudo qabas backup create --name "before-closing-2026"`,
		},
		Notes: []string{"Partial: --db (PostgreSQL only) or --s3 (object storage only). Preview with --dry-run."},
	}
	d.Sections = []Section{
		{Title: "Create", Where: here, Steps: []Step{installed, create}},
		{Title: "Find and inspect", Where: here, Steps: []Step{
			{Text: "List backups (filter by name regex, custom columns, or JSON).",
				Commands: []string{
					"qabas backup ls",
					"qabas backup ls --name '^pre-deploy'",
					"qabas backup ls --format 'table {{.ID}}\\t{{.Name}}\\t{{.Release}}\\t{{.Created}}'",
					"qabas backup ls --json",
				}},
			{Text: "Show one backup's metadata (release, db/s3 included, verified).",
				Commands: []string{fmt.Sprintf("qabas backup show %s", v.Backup)}},
			{Text: "Verify integrity. Also accepts a wrapper directory, a vpg directory, a vpg .tar.gz, or a parent directory containing one.",
				Commands: []string{
					fmt.Sprintf("qabas backup verify %s", v.Backup),
					"qabas backup verify /mnt/offsite/qb-20260915-120000",
				}},
		}},
		{Title: "Copy off the machine", Where: here, Steps: []Step{
			{Text: "A backup on the same disk does not survive disk loss. Copy it to another disk, NAS or mount. " +
				"Selector: exact id, exact name, or a regex matching exactly one backup. " +
				"If dest is an existing directory the copy lands in dest/<id>/.",
				Commands: []string{
					fmt.Sprintf("qabas backup cp %s /mnt/offsite/", v.Backup),
					"qabas backup cp pre-deploy /tmp/pre-deploy-copy",
					fmt.Sprintf("qabas backup cp %s /mnt/offsite/ --dry-run", v.Backup),
				},
				Notes: []string{"--force replaces an existing destination. For automatic copies to a replica use `backup schedule enable --offsite`."}},
		}},
		{Title: "Delete old backups", Where: here, Steps: []Step{
			{Text: "Always preview with --dry-run first. Selectors are combined: positional ids, " +
				"--id REGEX, --name REGEX (repeatable, OR), then -v/--except ID-REGEX and " +
				"--except-name NAME-REGEX remove matches from the selection. Only -v/--except-name " +
				"means start from ALL backups.",
				Commands: []string{
					"qabas backup delete --name '^pre-deploy$' --dry-run",
					"sudo qabas backup delete --name '^pre-deploy$' -y",
					"sudo qabas backup delete --id 'qb-202609.*' -y",
					fmt.Sprintf("sudo qabas backup delete -v '%s' -y", v.Backup),
					"sudo qabas backup delete --except-name '^scheduled$' --dry-run",
					"qabas backup delete --all --dry-run",
				}},
		}},
		{Title: "Restore (destructive)", Where: here, Intro: []string{
			"Restore puts the system in maintenance mode, takes an automatic pre-restore safety " +
				"backup, then replaces the FULL PostgreSQL cluster (all databases and roles) and/or " +
				"object storage. Users are disconnected.",
		}, Steps: []Step{
			{Text: "Pick and verify the backup.",
				Commands: []string{"qabas backup ls", fmt.Sprintf("qabas backup verify %s", v.Backup)}},
			{Text: "Restore everything from a QabasOne backup (asks for confirmation; -y skips it).",
				Commands: []string{fmt.Sprintf("sudo qabas restore %s", v.Backup), fmt.Sprintf("sudo qabas backup restore %s", v.Backup)}},
			{Text: "Restore only the database or only object storage.",
				Commands: []string{fmt.Sprintf("sudo qabas restore %s --db", v.Backup), fmt.Sprintf("sudo qabas restore %s --s3", v.Backup)}},
			{Text: "Restore from an external vpg artifact (directory or .tar.gz). These contain PostgreSQL only; --s3 is refused.",
				Commands: []string{
					"sudo qabas restore /tmp/backup/postgres_cluster_20260915.tar.gz --yes",
					"sudo qabas restore /mnt/offsite/qb-20260915-120000/ --yes",
				}},
			{Text: "Check the result. If it went wrong, restore the pre-restore safety backup the same way.",
				Commands: []string{"qabas status", "qabas doctor", "qabas backup ls --name '^pre-restore$'"}},
		}},
	}
	if f.Detected && len(f.Standbys()) > 0 {
		d.Sections = append(d.Sections, Section{
			Title: "Clusters: restore on the writer, then re-seed replicas", Where: writerWhere(f),
			Steps: []Step{{
				Text: "A restore rewrites the writer's cluster, so physical replicas must be re-seeded.",
				Commands: []string{
					fmt.Sprintf("sudo qabas replica add %s --rebuild -y", v.Replica),
					fmt.Sprintf("sudo qabas replica verify %s", v.Replica),
				},
			}},
		})
	}
	d.Troubleshooting = []QA{
		{Problem: "vpg not found on PATH", Fix: "Install the vpg tool on the host (qabas does not install it) and make sure `sudo` keeps it on PATH (e.g. /usr/local/bin)."},
		{Problem: "QabasOne is not installed", Fix: "Backups need an installed stack.", Commands: []string{"sudo qabas install"}},
		{Problem: "selector matches more than one backup (cp)", Fix: "Use the exact id or a tighter regex anchored with ^ and $.", Commands: []string{"qabas backup ls --format '{{.ID}} {{.Name}}'"}},
		{Problem: "external vpg backups contain PostgreSQL only; omit --s3", Fix: "Only QabasOne wrappers (with meta.yaml) contain object storage."},
		{Problem: "another qabas operation holds the lock", Fix: "Commands wait for a healthy holder and stop a stuck one (stopped, no heartbeat, or over its runtime limit) automatically; RESTORE/SWITCH/COMMIT are never interrupted. Never delete the lock file.", Commands: []string{"sudo qabas lock status", "qabas history"}},
	}
	d.SeeAlso = []string{"qabas guide schedule", "qabas guide pitr", "qabas backup --help"}
	return d
}

func scheduleTopic() Topic {
	return Topic{
		ID:      "schedule",
		Aliases: []string{"backup-schedule", "scheduled", "offsite", "off-site"},
		Title:   "Automatic daily backups with retention and off-site copies",
		Summary: "A systemd timer runs `qabas backup schedule run` daily on the writer: full backup, " +
			"verify, grandfather-father-son pruning, and (optionally) a copy streamed over SSH " +
			"to a registered replica. Only backups labelled \"scheduled\" are ever pruned; " +
			"manual, pre-deploy and pre-restore backups are kept.",
		Build: buildSchedule,
	}
}

func buildSchedule(f Facts, v Vars) Doc {
	w := writerWhere(f)
	d := Doc{Intro: []string{
		"Short path: `backup schedule enable` -> `backup schedule run` once to test -> " +
			"`backup schedule status`. Add --offsite <replica> once you have a replica.",
	}}
	if f.Detected && f.Role == RoleReplica {
		d.Intro = append(d.Intro, "This machine is a replica; scheduled runs exit quietly here. Enable the schedule on the writer ("+f.Writer+").")
	}
	when := "daily at " + f.ScheduleAt
	if f.ScheduleTZ != "" {
		when += " (" + f.ScheduleTZ + ")"
	}
	enabled := progress(f, f.ScheduleEnabled, when)
	offsite := Step{
		Text: "Copy every verified backup to a replica (kept under " + backup.OffsiteRoot + "/<writer> " +
			"there with the same retention). Requires a registered replica.",
		Commands: []string{fmt.Sprintf("sudo qabas backup schedule enable --offsite %s", v.Offsite)},
		Notes:    []string{`Disable off-site copies with --offsite "".`},
	}
	if f.Detected {
		switch {
		case f.Offsite != "":
			offsite.Status = done("to " + f.Offsite)
		case len(f.Standbys()) == 0:
			offsite.Status = blocked("no replica registered; see qabas guide replica")
		default:
			offsite.Status = todo("")
		}
	}
	d.Sections = []Section{
		{Title: "Enable", Where: w, Steps: []Step{
			{Text: "Install the daily timer with defaults (time and retention shown after enabling).",
				Status: enabled, Commands: []string{"sudo qabas backup schedule enable"}},
			{Text: "Choose the time (in the schedule timezone, default " + config.DefaultTimezone + ", up to 10 minutes random delay) and retention: " +
				"keep the newest backup of each of the last N days, ISO weeks and months (counted in the same timezone).",
				Commands: []string{
					"sudo qabas backup schedule enable --at 02:15 --keep-daily 14 --keep-weekly 8 --keep-monthly 12",
					"sudo qabas backup schedule enable --at 02:15 --timezone UTC",
				}},
			offsite,
		}},
		{Title: "Test and monitor", Where: w, Steps: []Step{
			{Text: "Run one scheduled backup now (exactly what the timer runs). It waits up to 2h for other qabas operations such as a deploy.",
				Commands: []string{"sudo qabas backup schedule run", "sudo qabas backup schedule run --dry-run"}},
			{Text: "Show schedule, next run, last result, local and off-site retained backups (--no-remote skips contacting the replica).",
				Commands: []string{"qabas backup schedule status", "qabas backup schedule status --no-remote", "qabas backup schedule status --json"}},
			{Text: "`qabas doctor` and `qabas status` warn when no successful scheduled backup happened in 36h or the off-site push failed.",
				Commands: []string{"qabas doctor", "journalctl -u qabas-backup.service -n 100"}},
		}},
		{Title: "Change or disable", Where: w, Steps: []Step{
			{Text: "Re-run enable with only the flags to change; others keep their saved values.",
				Commands: []string{"sudo qabas backup schedule enable --keep-daily 30"}},
			{Text: "Remove the timer. Existing backups are kept.",
				Commands: []string{"sudo qabas backup schedule disable"}},
		}},
	}
	d.Troubleshooting = []QA{
		{Problem: "scheduled backups run on the writer; this node is X", Fix: "Enable the schedule on the writer."},
		{Problem: "--offsite X is not a registered replica", Fix: "Use a name from the registry, or add the replica first.", Commands: []string{"qabas replica ls"}},
		{Problem: "not enough disk for a backup: N free, need M", Fix: "The run needs 1.5x the last scheduled backup (min 2 GiB). Free space under the backups directory or lower --keep-*.",
			Commands: []string{"sudo qabas backup schedule enable --keep-daily 5"}},
		{Problem: "last off-site push to X failed", Fix: "Check SSH/WireGuard to the replica and its disk, then run once by hand.",
			Commands: []string{"sudo qabas replica status", "sudo qabas backup schedule run"}},
		{Problem: "installed backup timer is not pinned to <zone>", Fix: "The timer predates the timezone setting or was edited by hand; re-run enable to rewrite it.",
			Commands: []string{"sudo qabas backup schedule enable"}},
		{Problem: "systemd cannot use timezone X", Fix: "Install timezone data on the host, or choose another IANA zone.",
			Commands: []string{"sudo apt-get install -y tzdata", "sudo qabas backup schedule enable --timezone UTC"}},
		{Problem: "no scheduled backup has run yet", Fix: "Check the timer is active and the next run time.", Commands: []string{"systemctl list-timers 'qabas-backup*'", "qabas backup schedule status"}},
	}
	d.SeeAlso = []string{"qabas guide backup", "qabas guide pitr", "qabas backup schedule --help"}
	return d
}

func pitrTopic() Topic {
	return Topic{
		ID:      "pitr",
		Aliases: []string{"point-in-time", "wal", "wal-archive"},
		Title:   "Point-in-time recovery (WAL archive + base backups)",
		Summary: "Continuous WAL archiving (the wal-archiver container, pg_receivewal on slot " +
			"qabas_wal_archive) plus weekly base backups let you recover the database to any " +
			"second, for example just before a wrong posting or a DROP. Replicas copy mistakes " +
			"within seconds; the archive keeps history.",
		Build: buildPITR,
	}
}

func buildPITR(f Facts, v Vars) Doc {
	w := writerWhere(f)
	d := Doc{Intro: []string{
		"Short path: `backup pitr enable` -> `backup pitr base` -> `backup pitr status` -> " +
			"`backup pitr restore-test`. Keep the repository on a different disk or copy it off-site.",
	}}
	if f.Detected && f.Role == RoleReplica {
		d.Intro = append(d.Intro, "This machine is a replica: enable PITR on the writer ("+f.Writer+").")
	}
	d.Sections = []Section{
		{Title: "Enable", Where: w, Steps: []Step{
			{Text: "Enable the WAL archive: creates replication/backup roles, the repository, the PITR compose overlay, starts wal-archiver and installs the weekly base-backup timer.",
				Status:   progress(f, f.PITREnabled, "enabled"),
				Commands: []string{"sudo qabas backup pitr enable"}},
			{Text: "Take the first base backup now (WAL is only replayable on top of a base). --keep N prunes older bases (default 4, 0 keeps all).",
				Commands: []string{"sudo qabas backup pitr base", "sudo qabas backup pitr base --keep 8"}},
		}},
		{Title: "Monitor", Where: w, Steps: []Step{
			{Text: "Archiver state, slot, newest WAL segment age and base backups. Exits non-zero when there are problems (usable in monitoring).",
				Commands: []string{"sudo qabas backup pitr status", "sudo qabas backup pitr status --json"}},
		}},
		{Title: "Prove you can restore", Where: w, Steps: []Step{
			{Text: "Restore base + WAL into a throwaway container and check the schema. Run it regularly; it does not touch production.",
				Commands: []string{"sudo qabas backup pitr restore-test"}},
			{Text: "Recover up to a moment (RFC3339, UTC 'Z' or with offset), or from a specific base.",
				Commands: []string{
					"sudo qabas backup pitr restore-test --target-time 2026-09-30T08:15:00Z",
					"sudo qabas backup pitr restore-test --base <base-id> --target-time 2026-09-30T11:15:00+03:00",
				}},
		}},
		{Title: "Keep the archive safe", Where: w, Steps: []Step{
			{Text: "The repository lives under the state directory (or QABAS_PITR_DIR in .env). Put it on another disk, or copy it off-site regularly.",
				Commands: []string{"sudo rsync -a /var/lib/qabasone/pitr/ /mnt/offsite/pitr/"}},
		}},
	}
	d.Troubleshooting = []QA{
		{Problem: "PITR is not enabled", Fix: "Enable it first.", Commands: []string{"sudo qabas backup pitr enable"}},
		{Problem: "status: newest WAL is old / archiver not running", Fix: "Check the wal-archiver container logs and restart it.", Commands: []string{"qabas logs wal-archiver", "sudo qabas up wal-archiver"}},
		{Problem: "slot retained MiB keeps growing", Fix: "The archiver is not consuming WAL; the writer's disk will fill. Fix the archiver or check repository disk space."},
		{Problem: "weekly base backup timer not installed", Fix: "Schedule `qabas backup pitr base` yourself (cron/systemd), or re-run enable on a systemd host."},
	}
	d.SeeAlso = []string{"qabas guide backup", "qabas backup pitr --help"}
	return d
}
