package guide

import "fmt"

func installTopic() Topic {
	return Topic{
		ID:      "install",
		Aliases: []string{"setup", "auth", "deploy", "upgrade", "rollback", "downgrade", "update"},
		Title:   "Install, authenticate, upgrade, deploy and roll back QabasOne",
		Summary: "QabasOne runs as a Docker Compose stack on a Linux host. Product repos and images " +
			"are private, so qabas needs a GitHub token once (stored under /etc/qabasone, mode " +
			"0600). Every upgrade takes an automatic pre-upgrade backup and can be rolled back.",
		Build: buildInstall,
	}
}

func buildInstall(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		"Short path (new writer): `auth login` -> `install` -> `status` / `doctor`. " +
			"Later: `upgrade --check` -> `upgrade` (clusters: then `replica sync --all --restart`).",
	}}
	if f.Detected && f.Role == RoleReplica {
		d.Intro = append(d.Intro, "This machine is a replica: never install, deploy or upgrade here. Do it on the writer ("+f.Writer+") and run `qabas replica sync --all --restart` there.")
	}
	if f.Detected && f.Role == RoleFresh {
		d.Intro = append(d.Intro, "QabasOne is not installed here. If this machine should become a REPLICA of an existing writer, stop and follow `qabas guide replica` instead (installing here would block replica provisioning).")
	}
	host := Step{
		Text:   "Linux host (amd64/arm64) with Docker Engine + compose v2. Recommended 2+ CPUs, 2+ GiB RAM, 10+ GiB disk.",
		Status: check(f, f.OS == "linux" && f.Docker && f.Compose, fmt.Sprintf("os=%s docker=%v compose=%v", f.OS, f.Docker, f.Compose)),
		Commands: []string{
			"curl -fsSL https://get.docker.com | sudo sh",
			"curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only",
			"qabas version",
		},
	}
	d.Sections = []Section{
		{Title: "Prepare and authenticate (once)", Where: WhereHere, Steps: []Step{
			host,
			{Text: "Save GitHub + GHCR credentials (one PAT with repo + read:packages). -E keeps GITHUB_TOKEN through sudo.",
				Commands: []string{
					`export GITHUB_TOKEN="$(gh auth token)"`,
					"sudo -E qabas auth login",
					"sudo qabas auth status",
				},
				Notes: []string{"Separate GHCR token or user: `sudo -E qabas auth set ghcr --username <github-user>`. Remove with `sudo qabas auth logout`."}},
		}},
		{Title: "Install", Where: WhereHere, Steps: []Step{
			{Text: "For unattended installs set the domains first (otherwise install asks).",
				Commands: []string{
					"sudo qabas config set domain example.com",
					"sudo qabas config set domains.api api.example.com",
					"qabas config validate",
				}},
			{Text: "Install the latest stable release (or a version / the dev channel). Preview with --dry-run.",
				Status: progress(f, f.Installed, f.Release),
				Commands: []string{
					"sudo qabas install",
					"sudo qabas install 2.8.0",
					"sudo qabas install --non-interactive",
					"sudo qabas install --dry-run",
				}},
			{Text: "Check health.",
				Commands: []string{"qabas status", "qabas ps", "qabas doctor"}},
		}},
		{Title: "Upgrade", Where: writerWhere(f), Steps: []Step{
			{Text: "See what is available and preview.",
				Commands: []string{"qabas release current", "qabas release ls", "sudo qabas upgrade --check"}},
			{Text: "Upgrade to the latest release on your channel, or to a specific version. A pre-upgrade backup is taken automatically (--no-backup skips it; not recommended).",
				Commands: []string{"sudo qabas upgrade", "sudo qabas upgrade 2.9.0"}},
			{Text: "Deploy an exact version or channel (also used to re-apply the current one).",
				Commands: []string{"sudo qabas deploy 2.8.0", "sudo qabas deploy dev", "sudo qabas deploy api --dev"}},
		}},
		{Title: "Undo a bad release", Where: writerWhere(f), Steps: []Step{
			{Text: "Roll back to the previous release, or name it.",
				Commands: []string{"sudo qabas rollback", "sudo qabas rollback --to 2.7.0 --yes"}},
			{Text: "Downgrade to an older version explicitly.",
				Commands: []string{"sudo qabas downgrade 2.7.0"}},
			{Text: "If the database migration itself failed, see `qabas guide maintenance` (db migrate --resolve-*), or restore the pre-upgrade backup.",
				Commands: []string{"qabas backup ls --name 'pre-deploy'", "qabas guide backup"}},
		}},
	}
	if !f.Detected || len(f.Standbys()) > 0 {
		d.Sections = append(d.Sections, Section{
			Title: "Clusters: propagate the release to replicas", Where: writerWhere(f),
			Steps: []Step{{
				Text:     "After any install/upgrade/deploy/rollback on the writer. Replicas never run migrations.",
				Commands: []string{"sudo qabas replica sync --all --restart", fmt.Sprintf("sudo qabas replica verify %s", v.Replica)},
			}},
		})
	}
	d.Sections = append(d.Sections, Section{
		Title: "Keep the CLI itself current", Where: WhereHere, Steps: []Step{
			{Text: "Update the qabas binary (separate from the product release).",
				Commands: []string{"sudo qabas update", "qabas version"}},
		},
	})
	d.Troubleshooting = []QA{
		{Problem: "HTTP 401/404 fetching releases or pulling images", Fix: "Token missing, expired, or lacking repo/read:packages.", Commands: []string{"sudo qabas auth status", "sudo -E qabas auth login"}},
		{Problem: "QabasOne is not installed", Fix: "Install first.", Commands: []string{"sudo qabas install"}},
		{Problem: "docker compose plugin is required", Fix: "Install Docker with the compose v2 plugin.", Commands: []string{"curl -fsSL https://get.docker.com | sudo sh"}},
		{Problem: "another qabas operation is running (lock held)", Fix: "The command waits for a healthy holder (a scheduled backup can take a while) and takes over from a stuck one automatically. See who holds it; never delete the lock file.", Commands: []string{"sudo qabas lock status", "qabas history"}},
		{Problem: "upgrade failed half-way", Fix: "Read the error and `qabas doctor`; then roll back.", Commands: []string{"qabas doctor --verbose", "sudo qabas rollback"}},
	}
	d.SeeAlso = []string{"qabas guide maintenance", "qabas guide backup", "qabas install --help"}
	return d
}
