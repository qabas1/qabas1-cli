package history

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/qabas1/qabasone-cli/internal/clilog"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/redact"
)

// history.jsonl is rotated into history.jsonl.N.gz once it reaches either cap.
const (
	MaxBytes   = 5 << 20
	MaxEntries = 10000
	Keep       = 3
)

type Entry struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Backup  string    `json:"backup,omitempty"`
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
}

func Append(p paths.Paths, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Message = redact.String(e.Message)
	if err := os.MkdirAll(p.StateDir, 0o750); err != nil {
		return err
	}
	if _, err := Enforce(p); err != nil {
		return err
	}
	f, err := os.OpenFile(p.HistoryFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(e)
}

// Enforce rotates history.jsonl when it exceeds MaxBytes or MaxEntries.
func Enforce(p paths.Paths) (bool, error) {
	st, err := os.Stat(p.HistoryFile())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	// Short entries average ~150 bytes, so counting lines below 1 MiB is wasted work.
	over := st.Size() >= MaxBytes
	if !over && st.Size() >= 1<<20 {
		raw, err := os.ReadFile(p.HistoryFile())
		if err != nil {
			return false, err
		}
		over = bytes.Count(raw, []byte("\n")) >= MaxEntries
	}
	if !over {
		return false, nil
	}
	return true, clilog.Rotate(p.HistoryFile(), Keep)
}

// Archives lists rotated history files, newest first.
func Archives(p paths.Paths) []string { return clilog.Archives(p.HistoryFile(), Keep) }

// Size is the bytes used by history.jsonl and its archives.
func Size(p paths.Paths) int64 { return clilog.Size(p.HistoryFile(), Keep) }

// List returns entries from the live history file.
func List(p paths.Paths) ([]Entry, error) {
	f, err := os.Open(p.HistoryFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return decode(f)
}

// ListAll returns entries from rotated archives (oldest first) followed by the live file.
func ListAll(p paths.Paths) ([]Entry, error) {
	var out []Entry
	archives := Archives(p)
	for i := len(archives) - 1; i >= 0; i-- {
		items, err := readArchive(archives[i])
		if err != nil {
			return out, err
		}
		out = append(out, items...)
	}
	live, err := List(p)
	return append(out, live...), err
}

func readArchive(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return decode(zr)
}

func decode(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
