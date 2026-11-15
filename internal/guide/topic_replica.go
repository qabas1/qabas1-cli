package guide

import (
	"fmt"
	"strings"
)

const hostKeyListCmd = `for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done`

func replicaTopic() Topic {
	return Topic{
		ID:      "replica",
		Aliases: []string{"replicas", "add-replica", "standby", "mirror"},
		Title:   "Add a machine (for example this one) as a read-only replica",
		Summary: "A replica is a PostgreSQL hot standby plus one-way MinIO replication, " +
			"reached over a WireGuard mesh. It serves the full UI read-only (login, browse, " +
			"reports, print) and can later be promoted to writer with `qabas dr`. " +
			"Every `qabas replica` command runs ON THE WRITER: give it the new machine's address " +
			"and an SSH key with root access, and it installs everything there itself (OS " +
			"updates, Docker, WireGuard, the qabas CLI, the compose stack, images, data). " +
			"Machines with no inbound access join with `qabas replica invite`.",
		Build: buildReplica,
	}
}

func buildReplica(f Facts, v Vars) Doc {
	var d Doc
	switch {
	case f.Detected && f.Role == RoleReplica:
		d = replicaOnReplica(f, v)
	case f.Detected && f.Role == RoleWriter:
		d = replicaOnWriter(f, v)
	case f.Detected && f.Role == RoleSingle:
		d = replicaOnSingle(f, v)
	default:
		d = replicaOnTarget(f, v)
	}
	d.Troubleshooting = replicaTroubleshooting(v)
	d.SeeAlso = []string{"qabas guide dr", "qabas guide schedule", "qabas guide install", "qabas replica --help"}
	return d
}

// replicaOnTarget: this machine is (or will be) the new replica.
func replicaOnTarget(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		"Short path: (1) make sure the writer can SSH into THIS machine as root (or a " +
			"passwordless-sudo user), (2) on the writer run `qabas replica add` - it creates the " +
			"cluster registry if needed (--writer-endpoint), updates this machine, installs " +
			"Docker and the qabas CLI, and provisions it, (3) verify. Commands below are " +
			"pre-filled with this machine's name, address and SSH host key fingerprint.",
		"No inbound access at all (NAT, no port forward)? Use the invite flow below instead.",
		"Do NOT run `qabas install` on this machine: the writer installs the stack itself, " +
			"and an existing QabasOne stack makes provisioning stop (preflight).",
	}}
	d.Sections = append(d.Sections,
		targetPrepSection(f, v),
		writerRegistrySection(f, v),
		addSection(f, v, true),
		inviteSection(v),
		verifySection(f, v),
		Section{
			Title: "Check from the replica itself", Where: WhereHere,
			Steps: []Step{{
				Text:     "Once READY, this machine shows its replica role and replication lag.",
				Commands: []string{"qabas status", "qabas guide replica", "qabas dr status"},
			}},
		},
		ongoingSection(f, v),
		removeSection(v),
	)
	return d
}

func targetPrepSection(f Facts, v Vars) Section {
	disk := "Free disk space: at least 2x the writer's database size + 5 GiB."
	if f.DiskFreeMiB > 0 {
		disk += " Detected " + mib(f.DiskFreeMiB) + " free here."
	}
	stackOK := len(f.Stack) == 0 && !f.Installed
	stackWhy := "QabasOne is installed here"
	if len(f.Stack) > 0 {
		shown := f.Stack
		if len(shown) > 3 {
			shown = shown[:3]
		}
		stackWhy = fmt.Sprintf("%d qabasone containers present (%s", len(f.Stack), strings.Join(shown, ", "))
		if len(f.Stack) > 3 {
			stackWhy += ", ..."
		}
		stackWhy += ")"
	}
	sshd := Status{}
	if f.SSHD != nil {
		sshd = check(f, *f.SSHD, "nothing listening on port 22")
	}
	archNote := "Physical replication needs the same CPU architecture on both machines. Compare `uname -m` here and on the writer."
	if f.Arch != "" {
		archNote += " This machine is " + f.Arch + "."
	}
	hostKey := Step{
		Text: "Note this machine's SSH host key fingerprint. The writer pins it on first " +
			"contact (pass it as --host-key so nothing is trusted blindly).",
		Commands: []string{hostKeyListCmd},
	}
	if f.HostKey != "" {
		hostKey.Status = done(f.HostKey)
	}
	return Section{
		Title: "Prepare this machine (the new replica)", Where: WhereHere,
		Steps: []Step{
			{Text: "Linux with systemd (WireGuard and the writer-lease timer are systemd units).",
				Status: check(f, f.OS == "linux" && f.Systemd, fmt.Sprintf("os=%s systemd=%v", f.OS, f.Systemd))},
			{Text: "Same CPU architecture as the writer.", Notes: []string{archNote},
				Commands: []string{"uname -m"}},
			{Text: "Docker Engine with the compose v2 plugin. `replica add` installs it (and runs " +
				"apt upgrade, reboots if needed, installs WireGuard and the qabas CLI) on Ubuntu " +
				"22.04+ / Debian 12+; install it yourself only for other distributions (with --no-bootstrap).",
				Status: optional(f, f.Docker && f.Compose, "installed by replica add"),
				Commands: []string{
					"docker compose version",
				}},
			{Text: disk, Commands: []string{"df -h /var/lib/docker /"}},
			{Text: "SSH server running and reachable from the writer (TCP 22, or pass --ssh-port).",
				Status: sshd,
				Commands: []string{
					"sudo apt-get install -y openssh-server",
					"sudo systemctl enable --now ssh",
				}},
			{Text: "An SSH user that is root or has passwordless sudo (default user: root; otherwise pass --ssh-user).",
				Commands: []string{
					`echo "$USER ALL=(ALL) NOPASSWD:ALL" | sudo tee /etc/sudoers.d/qabas-replica`,
					"sudo -n true && echo passwordless-sudo-ok",
				}},
			{Text: "Authorize an SSH key for that user (cloud providers usually do this at creation). " +
				"The writer uses it once (--identity <private-key>), then installs its own cluster " +
				"key and no longer needs yours.",
				Notes: []string{"Or authorize the writer's root key: on the writer `sudo ssh-keygen -t ed25519` then `sudo cat /root/.ssh/id_ed25519.pub`. Then here:"},
				Commands: []string{
					"mkdir -p ~/.ssh && chmod 700 ~/.ssh",
					"echo '<writer-public-key>' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys",
				}},
			{Text: "Open the firewall: TCP 22 from the writer and UDP 51820 (WireGuard) inbound. " +
				"If only SSH reaches this machine (NAT with an SSH port forward), add --behind-nat to " +
				"`replica add`: it dials out over WireGuard and SSH moves onto the tunnel. With no " +
				"inbound access at all, use `qabas replica invite`.",
				Commands: []string{"sudo ufw allow 22/tcp", "sudo ufw allow 51820/udp"}},
			{Text: "No QabasOne stack on this machine (do not run `qabas install` here). " +
				"If one exists, its data is discarded by `replica add --rebuild`, or remove it first.",
				Status:   check(f, stackOK, stackWhy),
				Commands: []string{"docker ps -a --filter name=^qabas1-"}},
			hostKey,
		},
	}
}

func writerRegistrySection(f Facts, v Vars) Section {
	registry := Step{
		Text: "Register the writer in a new cluster registry (once per cluster). --endpoint is " +
			"the writer's public address that replicas reach on UDP 51820.",
		Commands: []string{fmt.Sprintf("sudo qabas replica init --name %s --endpoint <writer-public-host>:51820", v.Writer)},
		Notes:    []string{"Skip this if `qabas replica ls` on the writer already shows a cluster."},
	}
	if f.Detected && f.Role == RoleWriter {
		registry.Status = done("registry exists, cluster " + f.Cluster)
	} else if f.Detected && f.Role == RoleSingle {
		registry.Status = todo("")
	}
	return Section{
		Title: "Prepare the writer (once)", Where: writerWhere(f),
		Steps: []Step{
			{Text: "Check the writer is healthy and has registry credentials (replicas pull images with them).",
				Commands: []string{"qabas status", "qabas auth status"}},
			registry,
			{Text: "Test SSH from the writer to the new replica as the user qabas will use.",
				Commands: []string{fmt.Sprintf("sudo ssh -i <setup-key> root@%s 'sudo -n true && echo ok'", v.Host)}},
		},
	}
}

func addSection(f Facts, v Vars, forThisMachine bool) Section {
	title := "Provision the replica"
	if forThisMachine {
		title = "Provision this machine as a replica"
	}
	add := Step{
		Text: "Run the resumable provisioning. It walks PREFLIGHT (read-only checks), NETWORK " +
			"(WireGuard), UPSTREAM (replication role, slot, pg_hba), STACK, SEED (pg_basebackup), " +
			"STORAGE (MinIO replication), START (read-only stack) and VERIFY.",
		Notes: []string{
			"BOOTSTRAP updates the new machine (apt upgrade, reboot if needed), installs Docker, " +
				"WireGuard and the qabas CLI at the writer's version, and installs the cluster SSH key. " +
				"Pass the setup key with --identity <private-key>; --no-os-upgrade / --no-reboot / " +
				"--no-bootstrap tune it.",
			"The first replica restarts the writer's PostgreSQL once (you are asked to confirm). " +
				"The writer must be healthy before anything is changed on it.",
			"No registry yet? Add --writer-endpoint <writer-public-host>:51820 to create it on the fly.",
			"Options: --ssh-user ops (non-root), --ssh-port 2222, --priority 50 (promotion preference), " +
				"--behind-nat (no inbound UDP), --wg-endpoint host:port (port-forwarded WireGuard), " +
				"--upstream <node> (cascade from another replica to save WAN), --public-url, --dry-run.",
			"Drop --promotable for a read-only kiosk that must never become writer.",
		},
		Commands: []string{
			fmt.Sprintf("sudo qabas replica add %s --host %s --host-key %s --site %s --promotable", v.Name, v.Host, v.HostKey, v.Site),
		},
	}
	if f.Detected {
		if f.Role == RoleReplica {
			add.Status = done("this machine is replica " + f.Self)
		} else if n := f.Node(v.Name); n != nil && n.Phase == "READY" {
			add.Status = done("READY")
		}
	}
	resume := Step{
		Text: "If a phase fails, fix the cause (see Troubleshooting) and continue where it stopped. " +
			"Use --rebuild only to discard the replica's data and re-seed from scratch.",
		Commands: []string{
			fmt.Sprintf("sudo qabas replica add %s --resume", v.Name),
			fmt.Sprintf("sudo qabas replica add %s --rebuild -y", v.Name),
		},
	}
	if f.Detected {
		if n := f.Node(v.Name); n != nil && n.LastError != "" {
			resume.Status = todo("stopped at " + n.Phase + ": " + n.LastError)
		}
	}
	return Section{Title: title, Where: writerWhere(f), Steps: []Step{add, resume}}
}

func inviteSection(v Vars) Section {
	return Section{
		Title: "No inbound access at all (NAT, no port forward)", Where: WhereWriter,
		Steps: []Step{
			{Text: "On the writer, create a one-time join script (valid 2h by default) and wait for the machine to join. " +
				"The script holds a temporary WireGuard key: copy it securely and delete it after use.",
				Commands: []string{fmt.Sprintf("sudo qabas replica invite %s --site %s --promotable --wait", v.Name, v.Site)},
				Notes:    []string{"The script is written to /etc/qabasone/invites/" + v.Name + ".sh (or use --print / --out)."}},
			{Text: "On the new machine, run the script as root. It only needs outbound UDP to the writer's WireGuard port; " +
				"it prints this machine's SSH host key fingerprints for you to confirm on the writer.",
				Commands: []string{fmt.Sprintf("sudo sh %s.sh", v.Name)}},
			{Text: "The writer then provisions the machine over WireGuard (BOOTSTRAP ... VERIFY) and replaces the temporary " +
				"key with one generated on the machine. Without --wait, continue later; revoke an unused invite.",
				Commands: []string{
					fmt.Sprintf("sudo qabas replica add %s --resume", v.Name),
					fmt.Sprintf("sudo qabas replica invite %s --revoke", v.Name),
				}},
		},
	}
}

func verifySection(f Facts, v Vars) Section {
	return Section{
		Title: "Verify", Where: writerWhere(f),
		Steps: []Step{
			{Text: "Prove the replica rejects writes, is streaming, caught up, and has the same schema and objects.",
				Commands: []string{fmt.Sprintf("sudo qabas replica verify %s", v.Name)}},
			{Text: "Live replication status (stream state, lag, slot retention) and the registry.",
				Commands: []string{"sudo qabas replica status", "qabas replica ls"}},
		},
	}
}

func ongoingSection(f Facts, v Vars) Section {
	return Section{
		Title: "Keep replicas current", Where: writerWhere(f),
		Steps: []Step{
			{Text: "After every release change on the writer, push the release, config and registry " +
				"to replicas and recreate their stacks. Replicas never run migrations; schema arrives through WAL.",
				Commands: []string{"sudo qabas deploy <version>", "sudo qabas replica sync --all --restart"}},
			{Text: "Send a copy of every scheduled backup to the replica (off-site).",
				Commands: []string{fmt.Sprintf("sudo qabas backup schedule enable --offsite %s", v.Name)}},
			{Text: "Promotion, switchover and failback are done with `qabas dr` (see `qabas guide dr`).",
				Commands: []string{"qabas guide dr"}},
		},
	}
}

func removeSection(v Vars) Section {
	return Section{
		Title: "Remove a replica", Where: WhereWriter,
		Steps: []Step{
			{Text: "Detach it (its stack stops, shared secrets are wiped there).",
				Commands: []string{fmt.Sprintf("sudo qabas replica rm %s", v.Name)}},
			{Text: "Also delete its data volumes, or drop an unreachable node from the registry.",
				Commands: []string{
					fmt.Sprintf("sudo qabas replica rm %s --purge-data -y", v.Name),
					fmt.Sprintf("sudo qabas replica rm %s --force -y", v.Name),
				}},
		},
	}
}

func replicaOnWriter(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		"This machine is the writer of cluster " + f.Cluster + ". To add another machine, " +
			"either run `qabas guide replica` on that machine (it prints these commands with its " +
			"own address and host key filled in) or follow the steps below with --name/--host.",
	}}
	nodes := Section{Title: "Registered replicas", Where: WhereHere}
	for _, n := range f.Standbys() {
		desc := "host " + n.Host
		if n.Site != "" {
			desc += ", site " + n.Site
		}
		if n.Promotable {
			desc += ", promotable"
		} else {
			desc += ", read-only (not promotable)"
		}
		st := Step{Text: n.Name + " (" + desc + ")"}
		switch {
		case n.LastError != "":
			st.Status = todo("stopped at " + n.Phase + ": " + n.LastError)
			st.Commands = []string{fmt.Sprintf("sudo qabas replica add %s --resume", n.Name)}
		case n.Phase == "" || n.Phase == "READY":
			st.Status = done("READY")
			st.Commands = []string{fmt.Sprintf("sudo qabas replica verify %s", n.Name)}
		default:
			st.Status = todo("provisioning at " + n.Phase)
			st.Commands = []string{fmt.Sprintf("sudo qabas replica add %s --resume", n.Name)}
		}
		nodes.Steps = append(nodes.Steps, st)
	}
	if len(nodes.Steps) == 0 {
		nodes.Steps = []Step{{Text: "No replicas yet.", Status: todo("")}}
	}
	d.Sections = append(d.Sections,
		nodes,
		Section{
			Title: "Prepare the new machine", Where: "the new replica",
			Steps: []Step{{
				Text: "Ubuntu 22.04+ or Debian 12+ with systemd, same CPU architecture, an SSH user " +
					"that is root or has passwordless sudo, UDP 51820 inbound open, and no QabasOne " +
					"stack. Docker, WireGuard and the qabas CLI are installed by `replica add`. " +
					"Read the host key fingerprint there to pin it:",
				Commands: []string{hostKeyListCmd},
			}},
		},
		writerRegistrySection(f, v),
		addSection(f, v, false),
		inviteSection(v),
		verifySection(f, v),
		ongoingSection(f, v),
		removeSection(v),
	)
	return d
}

func replicaOnSingle(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		"This machine runs QabasOne on its own (no cluster registry). Two different goals:",
		"A) Keep this machine as the writer and ADD a replica: follow the steps below.",
		"B) Turn THIS machine into a replica of ANOTHER writer: its local QabasOne data is " +
			"discarded and re-seeded from that writer. Take a backup first, then on the other " +
			"writer run `qabas replica add <name> --host <this-ip> ... --rebuild -y`.",
	}}
	d.Sections = append(d.Sections,
		Section{
			Title: "Before you start", Where: WhereHere,
			Steps: []Step{{
				Text:     "Take a backup of this writer.",
				Status:   progress(f, f.Backups > 0, f.LatestBackup),
				Commands: []string{"sudo qabas backup create --name pre-replica"},
			}},
		},
		writerRegistrySection(f, v),
		Section{
			Title: "Prepare the new machine", Where: "the new replica",
			Steps: []Step{{
				Text: "Install the qabas CLI there (not `qabas install`) and follow its checklist; it " +
					"prints the exact `replica add` command with its address and host key.",
				Commands: []string{"qabas guide replica"},
			}},
		},
		addSection(f, v, false),
		verifySection(f, v),
		ongoingSection(f, v),
		removeSection(v),
	)
	return d
}

func replicaOnReplica(f Facts, v Vars) Doc {
	d := Doc{Intro: []string{
		fmt.Sprintf("This machine is already replica %s of writer %s (cluster %s). It serves the UI read-only.", f.Self, f.Writer, f.Cluster),
		"Do not run install, deploy, upgrade, db migrate or backup schedule here: releases " +
			"are pushed from the writer (`qabas replica sync`) and schema arrives through WAL.",
	}}
	d.Sections = append(d.Sections,
		Section{
			Title: "Health on this machine", Where: WhereHere,
			Steps: []Step{
				{Text: "Containers, replication lag and the writer lease as seen from here.",
					Commands: []string{"qabas status", "qabas ps", "qabas dr status"}},
			},
		},
		Section{
			Title: "Health and repair from the writer", Where: WhereWriter,
			Steps: []Step{
				{Text: "Verify and inspect replication.",
					Commands: []string{fmt.Sprintf("sudo qabas replica verify %s", v.Name), "sudo qabas replica status"}},
				{Text: "Re-push release/config, or re-seed the data from scratch.",
					Commands: []string{
						fmt.Sprintf("sudo qabas replica sync %s --restart", v.Name),
						fmt.Sprintf("sudo qabas replica add %s --rebuild -y", v.Name),
					}},
			},
		},
		Section{
			Title: "If the writer is down", Where: WhereHere,
			Steps: []Step{{
				Text:     "Promotion is a deliberate DR action. Read the dr guide first.",
				Commands: []string{"qabas guide dr", "qabas dr candidates"},
			}},
		},
		removeSection(v),
	)
	return d
}

func replicaTroubleshooting(v Vars) []QA {
	return []QA{
		{Problem: `preflight failed: OS is "Darwin"/"Windows" (Linux required) or "systemd is required"`,
			Fix: "Replicas must be Linux hosts with systemd. Use another machine or VM."},
		{Problem: "preflight failed: CPU architecture X differs from the writer's Y",
			Fix: "Physical replication needs identical architecture (amd64 vs arm64). Use a matching machine."},
		{Problem: "preflight failed: Docker Engine is not installed or not running / docker compose v2 plugin is missing",
			Fix:      "Only with --no-bootstrap: install Docker on the replica yourself, or drop --no-bootstrap so `replica add` installs it.",
			Commands: []string{fmt.Sprintf("sudo qabas replica add %s --resume", v.Name)}},
		{Problem: "preflight failed: distribution X is not supported for automatic setup / Ubuntu or Debian too old",
			Fix: "Automatic setup supports Ubuntu 22.04+ and Debian 12+. Upgrade the host, or install Docker + compose v2 and the qabas CLI yourself and pass --no-bootstrap."},
		{Problem: "preflight failed: X is the writer itself (same /etc/machine-id) / the host already belongs to cluster Y",
			Fix: "--host points at the writer or at a machine of another cluster. Check the address. A cloned VM keeps the same machine-id: regenerate it there (`sudo rm /etc/machine-id && sudo systemd-machine-id-setup`)."},
		{Problem: "BOOTSTRAP: apt-get update failed / dpkg lock timeout / dpkg --configure -a failed",
			Fix:      "The replica cannot reach its package mirrors, or another apt run holds the lock for more than 10 minutes. Fix it on the replica, then resume.",
			Commands: []string{"sudo apt-get update", "sudo dpkg --configure -a", fmt.Sprintf("sudo qabas replica add %s --resume", v.Name)}},
		{Problem: "BOOTSTRAP: Docker apt signing key fingerprint mismatch",
			Fix: "The key downloaded from download.docker.com is not Docker's published key (proxy interception or tampering). Nothing was installed; investigate the network path before resuming."},
		{Problem: "BOOTSTRAP: X did not come back after the reboot",
			Fix:      "Check the machine's console. When it is up again (same SSH host key), resume. Use --no-reboot to skip automatic reboots.",
			Commands: []string{fmt.Sprintf("sudo qabas replica add %s --resume", v.Name)}},
		{Problem: "BOOTSTRAP: the writer has no GitHub token to install the qabas CLI",
			Fix:      "Store the token on the writer (it is copied to the replica, 0600), then resume.",
			Commands: []string{"sudo -E qabas auth login", fmt.Sprintf("sudo qabas replica add %s --resume", v.Name)}},
		{Problem: "UPSTREAM: the writer is not healthy, refusing to change it / the writer has only N MiB free",
			Fix:      "Nothing was changed on the writer. Fix the writer first, then resume.",
			Commands: []string{"qabas status", "qabas doctor", fmt.Sprintf("sudo qabas replica add %s --resume", v.Name)}},
		{Problem: "refusing to create an unbounded replication slot on the writer",
			Fix: "You declined limiting max_slot_wal_keep_size. Without a limit a disconnected replica can fill the writer's disk. Re-run and accept (reload only, no restart), or set it in the writer's compose overlay."},
		{Problem: "this invite expired / X did not join before the deadline",
			Fix:      "Create a new invite (expired ones are revoked automatically). The machine needs outbound UDP to the writer's WireGuard port.",
			Commands: []string{fmt.Sprintf("sudo qabas replica invite %s --revoke", v.Name), fmt.Sprintf("sudo qabas replica invite %s --wait", v.Name)}},
		{Problem: "preflight failed: only N MiB free; need at least M MiB (2x database + 5 GiB)",
			Fix: "Free space under /var/lib/docker on the replica (or attach a bigger disk), then resume."},
		{Problem: "preflight failed: an existing QabasOne stack is present",
			Fix:      "`qabas install` was run on the replica, or it held an old stack. Re-seed it (its local data is discarded) or uninstall it there first.",
			Commands: []string{fmt.Sprintf("sudo qabas replica add %s --rebuild -y", v.Name)}},
		{Problem: "run replica commands on the writer (X); this node is Y",
			Fix: "You ran `qabas replica ...` on the replica. Run it on the writer named in the message."},
		{Problem: "no cluster registry",
			Fix:      "Create it on the writer first.",
			Commands: []string{fmt.Sprintf("sudo qabas replica init --name %s --endpoint <writer-public-host>:51820", v.Writer)}},
		{Problem: "writer has no WireGuard endpoint yet",
			Fix:      "Set the writer's public endpoint (host:port reachable over UDP by replicas).",
			Commands: []string{"sudo qabas replica init --endpoint <writer-public-host>:51820"}},
		{Problem: "ssh <host>: connection refused / i/o timeout",
			Fix: "Start sshd on the replica, open TCP 22 (or pass --ssh-port), and check the address with `ping`."},
		{Problem: "ssh: unable to authenticate / permission denied",
			Fix: "The writer's root key is not in the replica user's authorized_keys, or the user lacks passwordless sudo. Pass --ssh-user / --identity if you use a different user or key."},
		{Problem: "host key mismatch for <host> / refusing to trust an unverified host key non-interactively",
			Fix:      "Read the fingerprint on the replica itself and pass it with --host-key. A mismatch can mean a reinstalled host or a man-in-the-middle.",
			Commands: []string{hostKeyListCmd}},
		{Problem: "NETWORK or SEED phase hangs / WireGuard handshake never completes",
			Fix: "UDP 51820 must reach the replica (and the writer). Open it on both firewalls and cloud security groups, or re-add with --behind-nat so the replica dials out."},
		{Problem: "WireGuard blocked by the network: no handshake although plain UDP reaches the writer (`tcpdump` shows test packets but no WireGuard)",
			Fix: "Some ISPs drop WireGuard itself. `replica invite` and `replica add` then switch to the TLS relay automatically (after 20s / 60s without a handshake): " +
				"the writer runs qabas-relay.service on TCP 8443 (pinned certificate, per-node token) and the replica qabas-relay-client.service. " +
				"Allow TCP 8443 to the writer (firewall / provider panel). The replica needs a current qabas CLI. Relay nodes peer with the writer only and are not promotable. " +
				"--relay skips the direct attempt.",
			Commands: []string{
				fmt.Sprintf("sudo qabas replica invite %s --revoke", v.Name),
				fmt.Sprintf("sudo qabas replica invite %s --relay --wait", v.Name),
				"sudo qabas relay status",
				"journalctl -u qabas-relay -u qabas-relay-client -n 50",
			}},
		{Problem: "a previous provisioning of X stopped after PHASE",
			Fix:      "Continue it, or start over.",
			Commands: []string{fmt.Sprintf("sudo qabas replica add %s --resume", v.Name), fmt.Sprintf("sudo qabas replica add %s --rebuild -y", v.Name)}},
		{Problem: "replica X is already provisioned",
			Fix:      "Use sync to re-push config or a new release.",
			Commands: []string{fmt.Sprintf("sudo qabas replica sync %s --restart", v.Name)}},
		{Problem: "replica verify fails: lag / not caught up / slot retained GiB growing",
			Fix:      "Check the WireGuard link and disk on the replica; large retained WAL means the replica is disconnected.",
			Commands: []string{"sudo qabas replica status", "sudo qabas dr status"}},
	}
}
