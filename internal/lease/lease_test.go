package lease

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	live := Record{WriterNode: "hq", Epoch: 3, LeaseExpiresAt: now.Add(30 * time.Second)}
	expired := Record{WriterNode: "hq", Epoch: 3, LeaseExpiresAt: now.Add(-time.Second)}

	cases := []struct {
		name   string
		stored Record
		next   Record
		want   bool
	}{
		{"empty store accepts", Record{}, Record{WriterNode: "hq", Epoch: 1, LeaseExpiresAt: now.Add(time.Minute)}, true},
		{"renewal same writer", live, Record{WriterNode: "hq", Epoch: 3, LeaseExpiresAt: now.Add(time.Minute)}, true},
		{"same epoch other writer rejected", live, Record{WriterNode: "branch", Epoch: 3, LeaseExpiresAt: now.Add(time.Minute)}, false},
		{"new epoch while live rejected", live, Record{WriterNode: "branch", Epoch: 4, LeaseExpiresAt: now.Add(time.Minute)}, false},
		{"new epoch after expiry accepted", expired, Record{WriterNode: "branch", Epoch: 4, LeaseExpiresAt: now.Add(time.Minute)}, true},
		{"stale epoch rejected", expired, Record{WriterNode: "branch", Epoch: 2, LeaseExpiresAt: now.Add(time.Minute)}, false},
		{"invalid proposal rejected", Record{}, Record{Epoch: 1}, false},
	}
	for _, c := range cases {
		if _, got := Decide(c.stored, c.next, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestAcquireQuorum(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	mk := func(n string) *FileWitness {
		return &FileWitness{Label: n, Path: filepath.Join(dir, n+".json")}
	}
	a, b, c := mk("a"), mk("b"), mk("c")
	ctx := context.Background()

	hq := Record{WriterNode: "hq", Epoch: 1, LeaseExpiresAt: now.Add(time.Minute)}
	if out := Acquire(ctx, []Witness{a, b, c}, hq); !out.Granted || out.Accepted != 3 {
		t.Fatalf("initial acquire: %+v", out)
	}
	branch := Record{WriterNode: "branch", Epoch: 2, LeaseExpiresAt: now.Add(time.Minute)}
	if out := Acquire(ctx, []Witness{a, b, c}, branch); out.Granted {
		t.Fatalf("promotion must be refused while hq lease is live: %+v", out)
	}
	_, votes := Observe(ctx, []Witness{a, b, c}, now)
	if ok, w := ValidAtQuorum(votes, now); !ok || w != "hq" {
		t.Fatalf("ValidAtQuorum = %v %q", ok, w)
	}
	if out := Acquire(ctx, nil, branch); out.Granted || !out.LocalOnly {
		t.Fatalf("no witnesses must fail closed: %+v", out)
	}
	if out := Acquire(ctx, []Witness{a, b}, branch); out.Granted {
		t.Fatalf("two witnesses must fail closed: %+v", out)
	}
}

func TestHTTPWitnessRoundTrip(t *testing.T) {
	store := &FileWitness{Label: "w", Path: filepath.Join(t.TempDir(), "w.json")}
	srv := httptest.NewServer(Handler(store, "s3cret"))
	defer srv.Close()
	ctx := context.Background()

	bad := &HTTPWitness{Label: "w", URL: srv.URL, Token: "nope"}
	if _, err := bad.Get(ctx); err == nil {
		t.Fatal("wrong token must fail")
	}
	w := &HTTPWitness{Label: "w", URL: srv.URL, Token: "s3cret"}
	rec := Record{WriterNode: "hq", Epoch: 1, LeaseExpiresAt: time.Now().UTC().Add(time.Minute)}
	if _, ok, err := w.Propose(ctx, rec); err != nil || !ok {
		t.Fatalf("propose: ok=%v err=%v", ok, err)
	}
	other := Record{WriterNode: "branch", Epoch: 2, LeaseExpiresAt: time.Now().UTC().Add(time.Minute)}
	stored, ok, err := w.Propose(ctx, other)
	if err != nil || ok || stored.WriterNode != "hq" {
		t.Fatalf("conflicting propose: ok=%v stored=%+v err=%v", ok, stored, err)
	}
}
