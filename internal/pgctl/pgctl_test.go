package pgctl

import (
	"context"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/nodeexec"
)

func TestParseAndCompareLSN(t *testing.T) {
	if CompareLSN("0/3000148", "0/3000148") != 0 {
		t.Fatal("equal LSNs")
	}
	if CompareLSN("0/FFFFFFFF", "1/0") != -1 {
		t.Fatal("segment boundary ordering")
	}
	if CompareLSN("", "0/1") != -1 {
		t.Fatal("empty LSN must sort first")
	}
}

func TestGetStatusParsesStandby(t *testing.T) {
	f := nodeexec.NewFake("dr-1").OnOut("psql", "t|on|160004|3||0/5000060|0/5000060|2026-09-30T10:00:00Z|0|streaming|7421234567890123456\n")
	st, err := GetStatus(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !st.InRecovery || !st.TxReadOnly || st.Timeline != 3 || st.WalReceiver != "streaming" || st.Position() != "0/5000060" {
		t.Fatalf("unexpected status %+v", st)
	}
}

func TestReplicationAndSlotsParseArchiverColumns(t *testing.T) {
	f := nodeexec.NewFake("hq").
		OnOut("pg_stat_replication", "pg_receivewal|172.21.0.11|streaming|0/9000000||48060.5|async|51|0/9000000|0.002\n"+
			"qabas_dr1|10.66.0.2|streaming|0/9000000|0/9000000|0.1|async|77|0/9000000|0.05\n").
		OnOut("pg_replication_slots", "qabas_replica_dr1|t|0/9000000|reserved|0|-1|77\nqabas_wal_archive|t|0/9000000|reserved|0|-1|51\n")
	rows, err := Replication(context.Background(), f)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	r := rows[0]
	if r.Application != "pg_receivewal" || r.ClientAddr != "172.21.0.11" || r.PID != 51 || r.WriteLSN != "0/9000000" ||
		r.WriteLag != 0.002 || r.ReplayLSN != "" || r.ReplayLag != 48060.5 {
		t.Fatalf("archiver row: %+v", r)
	}
	if !strings.Contains(f.Log[0], "host(client_addr)") {
		t.Fatalf("client_addr must be printed without a netmask: %s", f.Log[0])
	}
	slots, err := Slots(context.Background(), f)
	if err != nil || len(slots) != 2 || slots[1].Name != "qabas_wal_archive" || slots[1].ActivePID != 51 || !slots[1].Active {
		t.Fatalf("slots %+v err %v", slots, err)
	}
}

func TestProbeWriteRejected(t *testing.T) {
	ro := nodeexec.NewFake("dr-1").On("psql", nodeexec.Result{ExitCode: 3, Stderr: "ERROR:  cannot execute CREATE TABLE in a read-only transaction"})
	if err := ProbeWriteRejected(context.Background(), ro); err != nil {
		t.Fatalf("read-only standby reported writable: %v", err)
	}
	rw := nodeexec.NewFake("dr-1").On("psql", nodeexec.Result{ExitCode: 0})
	if err := ProbeWriteRejected(context.Background(), rw); err == nil {
		t.Fatal("writable node passed the read-only proof")
	}
}

func TestRenderHBAIsScramOnlyAndPeerScoped(t *testing.T) {
	hba := RenderHBAWithGateways([]string{"172.21.0.0/16"}, []string{"172.21.0.1"}, []HBAEntry{{Node: "dr-1", CIDR: "10.66.0.2/32"}})
	for _, line := range strings.Split(hba, "\n") {
		if strings.HasPrefix(line, "host") && (strings.HasSuffix(line, " trust") || strings.HasSuffix(line, " md5") || strings.HasSuffix(line, " password")) {
			t.Fatalf("network line is not scram/reject: %q\n%s", line, hba)
		}
	}
	if !strings.Contains(hba, "hostssl replication  qabas_replica   10.66.0.2/32") {
		t.Fatalf("peer replication line missing:\n%s", hba)
	}
	if !strings.Contains(hba, "hostssl replication  qabas_replica   172.21.0.1/32") {
		t.Fatalf("docker-proxy gateway replication line missing:\n%s", hba)
	}
	if !strings.HasSuffix(strings.TrimSpace(hba), "reject") {
		t.Fatal("pg_hba must end with reject")
	}
}

func TestBaseBackupScriptRefusesNonEmptyWithoutRebuild(t *testing.T) {
	s := BaseBackupScript("postgres@sha256:abc", "vol", "10.66.0.1", "qabas_replica_dr_1", "qabas_dr_1", "/run/qabas/pg.env", false)
	if !strings.Contains(s, "use --rebuild") || strings.Contains(s, "PGPASSWORD=") {
		t.Fatalf("unsafe basebackup script:\n%s", s)
	}
	if !strings.Contains(s, "-R -S 'qabas_replica_dr_1'") {
		t.Fatalf("slot / recovery conf flags missing:\n%s", s)
	}
}
