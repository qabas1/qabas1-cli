// Package lease implements the writer lease: a (writerNode, epoch, leaseExpiresAt) record
// agreed by a majority of witnesses and mirrored to /etc/qabasone/dr/writer.json, which
// the API reads to decide PRIMARY vs FENCED.
//
// Witness rule (compare-and-set, evaluated atomically on each witness):
//   - renewal: same epoch and same writer -> accepted;
//   - new epoch: accepted only when the stored lease has expired (or was released) and
//     the epoch strictly advances. A partitioned old writer therefore loses its lease
//     before any other node can be granted one.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record is the lease document (same JSON shape the API parses).
type Record struct {
	WriterNode     string    `json:"writerNode"`
	Epoch          int64     `json:"epoch"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	RenewedAt      time.Time `json:"renewedAt,omitempty"`
}

func (r Record) Empty() bool { return r.WriterNode == "" && r.Epoch == 0 }

func (r Record) Valid(now time.Time) bool { return !r.Empty() && now.Before(r.LeaseExpiresAt) }

// Decide applies the witness rule. It returns the record the witness must store and
// whether next was accepted.
func Decide(stored, next Record, now time.Time) (Record, bool) {
	switch {
	case next.WriterNode == "" || next.Epoch <= 0:
		return stored, false
	case stored.Empty():
		return next, true
	case next.Epoch == stored.Epoch && next.WriterNode == stored.WriterNode:
		return next, true
	case next.Epoch > stored.Epoch && !stored.Valid(now):
		return next, true
	}
	return stored, false
}

// Witness is one vote.
type Witness interface {
	Name() string
	Get(ctx context.Context) (Record, error)
	// Propose applies Decide atomically and returns the stored record afterwards.
	Propose(ctx context.Context, next Record) (Record, bool, error)
}

// Vote is one witness outcome.
type Vote struct {
	Witness  string `json:"witness"`
	Accepted bool   `json:"accepted"`
	Stored   Record `json:"stored"`
	Error    string `json:"error,omitempty"`
}

// Outcome of a quorum round.
type Outcome struct {
	Votes    []Vote `json:"votes"`
	Accepted int    `json:"accepted"`
	Needed   int    `json:"needed"`
	Granted  bool   `json:"granted"`
	// LocalOnly is true when no witnesses are configured (single-site mode).
	LocalOnly bool `json:"localOnly"`
}

// Quorum is a strict majority of n.
func Quorum(n int) int { return n/2 + 1 }

// Acquire proposes next to every witness and reports whether a majority accepted.
// Fewer than three witnesses cannot provide the required independent quorum.
func Acquire(ctx context.Context, ws []Witness, next Record) Outcome {
	out := Outcome{Needed: Quorum(len(ws))}
	if len(ws) == 0 {
		out.LocalOnly = true
		return out
	}
	if len(ws) < 3 {
		return out
	}
	for _, w := range ws {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		stored, ok, err := w.Propose(wctx, next)
		cancel()
		v := Vote{Witness: w.Name(), Accepted: ok && err == nil, Stored: stored}
		if err != nil {
			v.Error = err.Error()
		}
		if v.Accepted {
			out.Accepted++
		}
		out.Votes = append(out.Votes, v)
	}
	out.Granted = out.Accepted >= out.Needed
	return out
}

// Observe reads every witness; the returned record is the highest-epoch valid view.
func Observe(ctx context.Context, ws []Witness, now time.Time) (Record, []Vote) {
	var best Record
	var votes []Vote
	for _, w := range ws {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		r, err := w.Get(wctx)
		cancel()
		v := Vote{Witness: w.Name(), Stored: r}
		if err != nil {
			v.Error = err.Error()
		} else if r.Epoch > best.Epoch {
			best = r
		}
		votes = append(votes, v)
	}
	return best, votes
}

// ValidAtQuorum reports whether a majority of witnesses currently hold a valid lease
// for some writer (i.e. promotion of another node is NOT yet safe).
func ValidAtQuorum(votes []Vote, now time.Time) (bool, string) {
	counts := map[string]int{}
	for _, v := range votes {
		if v.Error == "" && v.Stored.Valid(now) {
			counts[v.Stored.WriterNode]++
		}
	}
	for w, c := range counts {
		if c >= Quorum(len(votes)) {
			return true, w
		}
	}
	return false, ""
}

// ---- writer.json -------------------------------------------------------------------

// WriteFile atomically writes the node-local mirror read by the API (0644: no secrets).
func WriteFile(path string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadFile(path string) (Record, error) {
	var r Record
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// Render returns writer.json bytes (for pushing to remote nodes).
func Render(r Record) []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return append(b, '\n')
}

// ---- file witness ------------------------------------------------------------------

// FileWitness stores the record in a JSON file guarded by an O_EXCL lock file. Use on a
// path only the witness host writes (the HTTP witness server uses it as its store).
type FileWitness struct {
	Label string
	Path  string
	Now   func() time.Time
}

func (f *FileWitness) Name() string { return f.Label }

func (f *FileWitness) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().UTC()
}

func (f *FileWitness) Get(ctx context.Context) (Record, error) {
	r, err := ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, nil
	}
	return r, err
}

func (f *FileWitness) Propose(ctx context.Context, next Record) (Record, bool, error) {
	unlock, err := f.lock(ctx)
	if err != nil {
		return Record{}, false, err
	}
	defer unlock()
	stored, err := f.Get(ctx)
	if err != nil {
		return Record{}, false, err
	}
	res, ok := Decide(stored, next, f.now())
	if !ok {
		return stored, false, nil
	}
	if err := WriteFile(f.Path, res); err != nil {
		return stored, false, err
	}
	return res, true, nil
}

func (f *FileWitness) lock(ctx context.Context) (func(), error) {
	lp := f.Path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return nil, err
	}
	for {
		fh, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = fh.Close()
			return func() { _ = os.Remove(lp) }, nil
		}
		if st, serr := os.Stat(lp); serr == nil && time.Since(st.ModTime()) > 15*time.Second {
			_ = os.Remove(lp)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("witness %s: lock busy: %w", f.Label, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
