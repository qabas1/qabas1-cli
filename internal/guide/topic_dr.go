package guide

import "fmt"

func drTopic() Topic {
	return Topic{
		ID:      "dr",
		Aliases: []string{"failover", "switchover", "promote", "failback", "disaster-recovery"},
		Title:   "Disaster recovery: switchover, break-glass promote, rejoin, failback",
		Summary: "The writer lease renews every 20s. Strict DR requires three independent " +
			"witnesses, synchronous PostgreSQL commits, verified MinIO object durability, and " +
			"fencing of both data services. Review docs/dr-strict-consistency.md before enabling failover.",
		Build: buildDR,
	}
}

func buildDR(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		"Short path after meeting the strict consistency release gate: `dr status` -> `dr candidates` -> `dr switchover <node>` (writer up) or " +
			"`dr promote <node> --break-glass` (writer down) -> point users at the new writer " +
			"(`dr routing`) -> later `dr failback <old-writer>`.",
	}}
	if f.Detected {
		switch f.Role {
		case RoleWriter:
			d.Intro = append(d.Intro, fmt.Sprintf("This machine is the writer (%s, epoch %d).", f.Writer, f.Epoch))
		case RoleReplica:
			d.Intro = append(d.Intro, fmt.Sprintf("This machine is replica %s; the writer is %s.", f.Self, f.Writer))
		case RoleFresh, RoleSingle:
			d.Intro = append(d.Intro, "No cluster registry on this machine: DR needs at least one promotable replica (see `qabas guide replica`).")
		}
	}
	hasReplica := Step{
		Text:     "At least one replica added with --promotable, verified.",
		Status:   progress(f, len(f.Standbys()) > 0, v.Replica),
		Commands: []string{"qabas guide replica", fmt.Sprintf("sudo qabas replica verify %s", v.Replica)},
	}
	witnesses := Step{
		Text: "Lease witnesses (required 3 or more, on independently hosted persistent servers). " +
			"A witness count alone does not prove failure-domain independence.",
		Status: progress(f, f.Witnesses >= 3, fmt.Sprintf("%d witness(es)", f.Witnesses)),
		Commands: []string{
			"sudo qabas dr witness add cloud1 --url https://witness1.example.com:7443",
			"sudo qabas dr witness add cloud2 --url https://witness2.example.com:7443",
			"sudo qabas replica sync --all",
		},
	}
	d.Sections = []Section{
		{Title: "Be ready before a disaster", Where: writerWhere(f), Steps: []Step{
			hasReplica,
			witnesses,
			{Text: "Make sure the lease timer runs (installed by `replica add`; reinstall if missing).",
				Commands: []string{"sudo qabas dr lease show", "sudo qabas dr lease install", "systemctl status qabas-dr-lease.timer"}},
			{Text: "Rehearse: a planned switchover to the replica and a failback, in a quiet hour.",
				Commands: []string{fmt.Sprintf("sudo qabas dr switchover %s", v.Replica), fmt.Sprintf("sudo qabas dr failback %s", v.Writer)}},
		}},
		{Title: "Run a witness server", Where: WhereWitness, Steps: []Step{
			{Text: "`witness add --url` prints the token file to copy (mode 0600) from the writer. On the witness host run the server (TLS strongly recommended; without TLS expose it only over WireGuard/private network):",
				Commands: []string{"qabas dr witness serve --listen :7443 --store /var/lib/qabas-witness/lease.json --token-file /etc/qabas-witness/token --tls-cert cert.pem --tls-key key.pem"}},
		}},
		{Title: "Assess (never changes anything)", Where: WhereAny, Steps: []Step{
			{Text: "Writer, lease, replication, a WireGuard reachability matrix and a verdict.",
				Commands: []string{"sudo qabas dr status"}},
			{Text: "Promotable replicas ranked by replayed data (least loss first), then --priority.",
				Commands: []string{"sudo qabas dr candidates"}},
		}},
		{Title: "Writer is UP: planned switchover", Where: WhereWriter, Steps: []Step{
			{Text: "Use only after verifying synchronous PostgreSQL and MinIO durability, plus fencing. The current CLI's replay-LSN check alone cannot establish zero data loss across both systems.",
				Commands: []string{fmt.Sprintf("sudo qabas dr switchover %s", v.Replica), fmt.Sprintf("sudo qabas dr switchover %s --max-lag 120", v.Replica)},
				Notes:    []string{"--max-lag (default 30s) refuses when the target is too far behind. Preview with --dry-run."}},
		}},
		{Title: "Writer is DOWN: break-glass promote", Where: "the replica to promote", Intro: []string{
			"Requires at least three independent lease witnesses. Refused while the writer is reachable from any node (use switchover) or while a witness " +
				"majority still grants the old writer a valid lease. Do not promote until every acknowledged " +
				"PostgreSQL commit and MinIO object mutation is proven durable on the target and the old writer is fenced.",
		}, Steps: []Step{
			{Text: "Confirm the writer is really down and pick the best candidate.",
				Commands: []string{"sudo qabas dr status", "sudo qabas dr candidates"}},
			{Text: "Promote (asks you to type the node name).",
				Commands: []string{fmt.Sprintf("sudo qabas dr promote %s --break-glass", v.Replica)}},
			{Text: "Scripted form.",
				Commands: []string{fmt.Sprintf("sudo qabas dr promote %s --break-glass --confirm %s --non-interactive", v.Replica, v.Replica)}},
		}},
		{Title: "Point users at the new writer", Where: "each site's router host", Steps: []Step{
			{Text: "Render the site HAProxy config for the current writer and the DNS/tunnel notes, then reload HAProxy.",
				Commands: []string{
					fmt.Sprintf("sudo qabas dr routing --site %s --out /etc/haproxy/haproxy.cfg", v.Site),
					"qabas dr routing --site branch --api-domain api.example.com",
					"sudo systemctl reload haproxy",
				}},
		}},
		{Title: "Bring the old writer back", Where: "the current writer", Steps: []Step{
			{Text: "Make the former writer a standby of the current writer (pg_rewind; writes it made after the promotion are discarded). --rebuild re-seeds with pg_basebackup instead.",
				Commands: []string{fmt.Sprintf("sudo qabas dr rejoin %s", v.Writer), fmt.Sprintf("sudo qabas dr rejoin %s --rebuild -y", v.Writer)}},
			{Text: "Or return the writer role to it in one step (rejoin + switchover).",
				Commands: []string{fmt.Sprintf("sudo qabas dr failback %s", v.Writer)}},
			{Text: "Verify the cluster afterwards.",
				Commands: []string{"sudo qabas dr status", "sudo qabas replica status"}},
		}},
	}
	d.Troubleshooting = []QA{
		{Problem: "promote requires --break-glass", Fix: "If the writer is really down add --break-glass; if it is up use switchover."},
		{Problem: "promote refused: writer reachable / witness majority grants a valid lease", Fix: "The writer is not provably down. Use switchover, or wait for the lease to expire (check `dr lease show`)."},
		{Problem: "switchover refused: target lags more than N seconds", Fix: "Wait for it to catch up or raise --max-lag.", Commands: []string{"sudo qabas replica status"}},
		{Problem: "--confirm X is required non-interactively", Fix: "Pass --confirm with the exact target node name."},
		{Problem: "lease not renewed / writer self-fenced (API read-only)", Fix: "Strict renewal fenced this writer because quorum or peer verification failed. Temporary lease fencing does not mark PostgreSQL DEMOTED. Inspect the database container, lease and registry before recovery; do not assume manual renewal makes the database writable.", Commands: []string{"sudo qabas dr lease show", "sudo qabas dr status", "journalctl -u qabas-dr-lease -n 20 --no-pager"}},
		{Problem: "users see read-only / local copy while the primary's services are healthy",
			Fix: "The writer lease (writer.json) expired, so the API fenced itself. `qabas status` shows a writer-lease row. " +
				"The timer journal says why renewal failed. Verify PostgreSQL fencing and witness state before any recovery.",
			Commands: []string{"sudo qabas dr lease show", "journalctl -u qabas-dr-lease -n 20 --no-pager", "sudo qabas dr lease renew"}},
		{Problem: "rejoin fails with pg_rewind errors", Fix: "Re-seed the former writer instead.", Commands: []string{fmt.Sprintf("sudo qabas dr rejoin %s --rebuild -y", v.Writer)}},
	}
	d.SeeAlso = []string{"qabas guide replica", "qabas dr --help"}
	return d
}
