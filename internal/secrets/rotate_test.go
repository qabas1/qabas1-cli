package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

func rotateFixture(t *testing.T) (paths.Paths, Bundle) {
	t.Helper()
	d := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "state")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := Bundle{
		PostgresPassword: "pg-old-0123456789", RedisPassword: "redis-old-0123456789", MinioUser: "qabasone",
		PostgresAppPassword: "app-pg-old-0123456789",
		MinioPassword:       "minio-old-0123456789", JWTSecret: "jwt-old-0123456789", TwoFactorKey: "tfa-old-0123456789",
		ServiceAccountAPIKeyPepper: "pepper-old-0123456789",
	}
	if err := writeSecretsFile(p, b); err != nil {
		t.Fatal(err)
	}
	api := "DATABASE_URL=postgresql://qabas:" + b.PostgresPassword + "@postgres:5432/qabas\nREDIS_URL=redis://:" + b.RedisPassword + "@redis:6379\nJWT_SECRET=" + b.JWTSecret + "\n"
	if err := os.WriteFile(p.EnvAPI(), []byte(api), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, b
}

func TestRotateRuntimeDatabasePasswordRollsBackWithFiles(t *testing.T) {
	p, old := rotateFixture(t)
	api := "POSTGRES_URI=postgresql://qabasone_app:" + old.PostgresAppPassword + "@postgres:5432/qabasone\n"
	if err := os.WriteFile(p.EnvAPI(), []byte(api), 0o600); err != nil {
		t.Fatal(err)
	}
	var changed []string
	res, err := Rotate(p, []string{"postgres-app"}, RotateDeps{
		Generate:               counter(),
		SetPostgresAppPassword: func(pw string) error { changed = append(changed, pw); return nil },
		Apply:                  func() error { return nil },
		Verify:                 func() error { return errors.New("health failed") },
	})
	if err == nil || !res.RolledBack || len(changed) != 2 || changed[1] != old.PostgresAppPassword {
		t.Fatalf("runtime password rollback failed: res=%+v calls=%d err=%v", res, len(changed), err)
	}
	if got, _ := os.ReadFile(p.EnvAPI()); string(got) != api {
		t.Fatal("runtime env was not restored")
	}
}

func counter() func(int) (string, error) {
	n := 0
	return func(int) (string, error) {
		n++
		return "new-value-000000000" + string(rune('a'+n)), nil
	}
}

func TestRotateRewritesEverywhereAndStamps(t *testing.T) {
	p, old := rotateFixture(t)
	var pg []string
	applied := 0
	res, err := Rotate(p, []string{"postgres", "redis"}, RotateDeps{
		Generate:            counter(),
		Now:                 func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
		SetPostgresPassword: func(pw string) error { pg = append(pg, pw); return nil },
		Apply:               func() error { applied++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rotated) != 2 || len(res.Files) != 2 || applied != 1 {
		t.Fatalf("res=%+v applied=%d", res, applied)
	}
	api, _ := os.ReadFile(p.EnvAPI())
	sec, _ := os.ReadFile(p.SecretsFile())
	for _, body := range []string{string(api), string(sec)} {
		if strings.Contains(body, old.PostgresPassword) || strings.Contains(body, old.RedisPassword) {
			t.Fatalf("old value left behind:\n%s", body)
		}
	}
	if !strings.Contains(string(api), old.JWTSecret) {
		t.Fatal("unrelated secret changed")
	}
	if len(pg) != 1 || !strings.Contains(string(api), pg[0]) {
		t.Fatalf("database password %v not applied to env files", pg)
	}
	if st := LoadMeta(p)["postgres"]; st.RotatedAt.IsZero() {
		t.Fatal("rotation not stamped")
	}
}

func TestRotateRollsBackOnVerifyFailure(t *testing.T) {
	p, old := rotateFixture(t)
	before, _ := os.ReadFile(p.EnvAPI())
	var pg []string
	maint := []bool{}
	applied := 0
	res, err := Rotate(p, []string{"postgres"}, RotateDeps{
		Generate:            counter(),
		SetPostgresPassword: func(pw string) error { pg = append(pg, pw); return nil },
		Maintenance:         func(on bool) error { maint = append(maint, on); return nil },
		Apply:               func() error { applied++; return nil },
		Verify:              func() error { return errors.New("api unhealthy") },
	})
	if err == nil || !res.RolledBack {
		t.Fatalf("want rollback, got res=%+v err=%v", res, err)
	}
	after, _ := os.ReadFile(p.EnvAPI())
	if string(after) != string(before) {
		t.Fatalf("env not restored:\n%s", after)
	}
	if len(pg) != 2 || pg[1] != old.PostgresPassword {
		t.Fatalf("database password not restored: %v", pg)
	}
	if applied != 2 {
		t.Fatalf("applied=%d, want apply + re-apply", applied)
	}
	if len(maint) != 2 || !maint[0] || maint[1] {
		t.Fatalf("maintenance calls = %v", maint)
	}
}

func TestRotateBackupFailureChangesNothing(t *testing.T) {
	p, _ := rotateFixture(t)
	before, _ := os.ReadFile(p.SecretsFile())
	_, err := Rotate(p, []string{"redis"}, RotateDeps{Generate: counter(), Backup: func() (string, error) { return "", errors.New("disk full") }})
	if err == nil {
		t.Fatal("expected error")
	}
	after, _ := os.ReadFile(p.SecretsFile())
	if string(after) != string(before) {
		t.Fatal("secrets changed despite failed backup")
	}
}

func TestResolveNames(t *testing.T) {
	got, err := ResolveNames([]string{"all"}, false)
	if err != nil || strings.Join(got, ",") != "postgres,postgres-app,redis,minio,jwt" {
		t.Fatalf("all = %v, %v", got, err)
	}
	if _, err := ResolveNames([]string{"pepper"}, false); err == nil {
		t.Fatal("pepper must need --force")
	}
	if got, err := ResolveNames([]string{"2fa", "2fa"}, true); err != nil || len(got) != 1 {
		t.Fatalf("2fa --force = %v, %v", got, err)
	}
	if _, err := ResolveNames([]string{"nope"}, true); err == nil {
		t.Fatal("unknown name accepted")
	}
}

func TestMetaBackfillUsesMtime(t *testing.T) {
	p, _ := rotateFixture(t)
	mt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(p.SecretsFile(), mt, mt)
	m := LoadMeta(p)
	if !m.Backfill("redis", p.SecretsFile()) || !m["redis"].CreatedAt.Equal(mt) || !m["redis"].Backfilled {
		t.Fatalf("backfill = %+v", m["redis"])
	}
	if m.Backfill("redis", p.SecretsFile()) {
		t.Fatal("second backfill must be a no-op")
	}
}
