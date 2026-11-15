package cli

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/lease"
)

func TestWriterLeaseCheck(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	c := &cluster.Cluster{Writer: "hq", Nodes: []cluster.Node{{Name: "hq", Role: cluster.RolePrimary}, {Name: "dr1", Role: cluster.RoleStandby}}}
	valid := lease.Record{WriterNode: "hq", Epoch: 1, LeaseExpiresAt: now.Add(40 * time.Second)}
	expired := lease.Record{WriterNode: "hq", Epoch: 1, LeaseExpiresAt: now.Add(-5 * time.Minute)}

	if ck := writerLeaseCheck("dr1", c, valid, nil, now); ck != nil {
		t.Fatalf("standbys have no writer-lease row: %+v", ck)
	}
	if ck := writerLeaseCheck("hq", c, lease.Record{}, fs.ErrNotExist, now); ck != nil {
		t.Fatalf("no writer.json (single node) means no row: %+v", ck)
	}
	if ck := writerLeaseCheck("hq", c, valid, nil, now); ck == nil || !ck.OK {
		t.Fatalf("valid lease: %+v", ck)
	}
	ck := writerLeaseCheck("hq", c, expired, nil, now)
	if ck == nil || ck.OK || !strings.Contains(ck.Detail, "EXPIRED") || !strings.Contains(ck.Detail, "read-only") {
		t.Fatalf("expired lease: %+v", ck)
	}
	if ck := writerLeaseCheck("hq", c, lease.Record{WriterNode: "dr1", Epoch: 2, LeaseExpiresAt: now.Add(time.Minute)}, nil, now); ck == nil || ck.OK {
		t.Fatalf("lease held by another node: %+v", ck)
	}
	if ck := writerLeaseCheck("hq", c, lease.Record{}, errors.New("bad json"), now); ck == nil || ck.OK {
		t.Fatalf("unreadable writer.json: %+v", ck)
	}
}
