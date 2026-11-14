package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/doctor"
	"github.com/qabasone/qabasone-cli/internal/lease"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
)

// localWriterLease reports the writer lease when this node is the registered writer
// of a cluster that uses one; nil otherwise (single node, standby, no writer.json).
func localWriterLease(e *env, now time.Time) *doctor.Check {
	c, err := cluster.Load(e.Paths)
	if err != nil || c == nil {
		return nil
	}
	l := nodestack.LocalLayout(e.Paths.ConfigDir, e.Paths.StateDir)
	r, rerr := lease.ReadFile(l.WriterFile())
	return writerLeaseCheck(e.selfName(c), c, r, rerr, now)
}

func writerLeaseCheck(self string, c *cluster.Cluster, r lease.Record, readErr error, now time.Time) *doctor.Check {
	writer := c.Writer
	if writer == "" {
		if p := c.Primary(); p != nil {
			writer = p.Name
		}
	}
	if self == "" || self != writer || errors.Is(readErr, fs.ErrNotExist) {
		return nil
	}
	ck := &doctor.Check{Name: "writer-lease", Level: "error"}
	switch {
	case readErr != nil:
		ck.Detail = fmt.Sprintf("cannot read writer.json: %v (the API treats this node as read-only)", readErr)
	case r.WriterNode != self:
		ck.Detail = fmt.Sprintf("writer.json names %s, not %s (the API is read-only here)", r.WriterNode, self)
	case !r.Valid(now):
		ck.Detail = fmt.Sprintf("EXPIRED since %s (the API is read-only; see journalctl -u qabas-dr-lease)", r.LeaseExpiresAt.Local().Format("2006-01-02 15:04:05"))
	default:
		ck.OK, ck.Level = true, "info"
		ck.Detail = fmt.Sprintf("epoch %d, valid until %s", r.Epoch, r.LeaseExpiresAt.Local().Format("15:04:05"))
	}
	return ck
}
