package backup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func sampleMetas() []backup.Meta {
	return []backup.Meta{
		{ID: "qb-20260904-135032", Name: ""},
		{ID: "qb-20260904-204124", Name: "alqabasy_s1_init"},
		{ID: "qb-20260905-110043", Name: "pre-deploy"},
		{ID: "qb-20260914-010002", Name: "real_D14-09-2026_T04-00AM"},
		{ID: "mahros", Name: "keep-me"},
	}
}

func idsOf(ms []backup.Meta) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestSelectExactIDs(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{
		IDs: []string{"qb-20260904-135032", "mahros"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qb-20260904-135032", "mahros"}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectUnknownExactID(t *testing.T) {
	_, err := backup.Select(sampleMetas(), backup.SelectOptions{IDs: []string{"missing"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.BackupRestore {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.BackupRestore)
	}
}

func TestSelectIDRegex(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{
		IDPatterns: []string{`qb-20260904-.*`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qb-20260904-135032", "qb-20260904-204124"}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectNameRegex(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{
		NamePatterns: []string{`^pre-deploy$`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qb-20260905-110043"}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectEmptyNameNeverMatchesNameRegex(t *testing.T) {
	_, err := backup.Select(sampleMetas(), backup.SelectOptions{
		NamePatterns: []string{`^$`},
	})
	if err == nil {
		t.Fatal("expected no-match error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestSelectExceptID(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{Except: `^mahros$`})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"qb-20260904-135032",
		"qb-20260904-204124",
		"qb-20260905-110043",
		"qb-20260914-010002",
	}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectExceptName(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{ExceptName: `^pre-deploy$`})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"qb-20260904-135032",
		"qb-20260904-204124",
		"qb-20260914-010002",
		"mahros",
	}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectUnionThenExcept(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{
		NamePatterns: []string{`pre-deploy|keep-me`},
		Except:       `^mahros$`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qb-20260905-110043"}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestSelectAll(t *testing.T) {
	got, err := backup.Select(sampleMetas(), backup.SelectOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d want 5", len(got))
	}
}

func TestSelectNoSelectors(t *testing.T) {
	_, err := backup.Select(sampleMetas(), backup.SelectOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestSelectBadRegex(t *testing.T) {
	_, err := backup.Select(sampleMetas(), backup.SelectOptions{IDPatterns: []string{`(`}})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestSelectEmptyAfterExcept(t *testing.T) {
	_, err := backup.Select(sampleMetas(), backup.SelectOptions{
		IDs:    []string{"mahros"},
		Except: `^mahros$`,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestResolveOneExactID(t *testing.T) {
	got, err := backup.ResolveOne(sampleMetas(), "qb-20260905-110043")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "qb-20260905-110043" {
		t.Fatalf("got %s", got.ID)
	}
}

func TestResolveOneExactName(t *testing.T) {
	got, err := backup.ResolveOne(sampleMetas(), "pre-deploy")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "qb-20260905-110043" {
		t.Fatalf("got %s", got.ID)
	}
}

func TestResolveOneExactIDPreferredOverNameRegex(t *testing.T) {
	// Exact id wins even if the same string could be a broader regex.
	got, err := backup.ResolveOne(sampleMetas(), "mahros")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "mahros" {
		t.Fatalf("got %s", got.ID)
	}
}

func TestResolveOneRegexOneMatch(t *testing.T) {
	got, err := backup.ResolveOne(sampleMetas(), `^qb-20260914-`)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "qb-20260914-010002" {
		t.Fatalf("got %s", got.ID)
	}
}

func TestResolveOneRegexAmbiguous(t *testing.T) {
	_, err := backup.ResolveOne(sampleMetas(), `qb-20260904-`)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestResolveOneNotFound(t *testing.T) {
	_, err := backup.ResolveOne(sampleMetas(), `^does-not-exist$`)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.BackupRestore {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.BackupRestore)
	}
}

func TestResolveOneEmptySelector(t *testing.T) {
	_, err := backup.ResolveOne(sampleMetas(), "  ")
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestResolveOneBadRegex(t *testing.T) {
	_, err := backup.ResolveOne(sampleMetas(), `(`)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestFilterByName(t *testing.T) {
	got, err := backup.Filter(sampleMetas(), nil, []string{`pre-deploy|keep-me`})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qb-20260905-110043", "mahros"}; !equalStrings(idsOf(got), want) {
		t.Fatalf("got %v want %v", idsOf(got), want)
	}
}

func TestFilterNoPatternsReturnsAll(t *testing.T) {
	got, err := backup.Filter(sampleMetas(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d want 5", len(got))
	}
}

func TestFilterNoMatchEmpty(t *testing.T) {
	got, err := backup.Filter(sampleMetas(), nil, []string{`^does-not-exist$`})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d want 0", len(got))
	}
}

func TestFilterBadRegex(t *testing.T) {
	_, err := backup.Filter(sampleMetas(), nil, []string{`(`})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestDeleteDryRunAndRemove(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{
		ConfigDir: filepath.Join(root, "etc"),
		StateDir:  filepath.Join(root, "lib"),
		LogDir:    filepath.Join(root, "log"),
		LockPath:  filepath.Join(root, "qabas.lock"),
	}
	id := "qb-20260915-120000"
	dir := backup.Dir(p, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := backup.Meta{
		ID:              id,
		Name:            "test",
		QabasOneRelease: "1.0.0",
		CreatedAt:       time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
		Database:        backup.Part{Included: true, File: "postgres/data.tar.gz", Engine: "vpg"},
		Verified:        true,
	}
	b, err := yaml.Marshal(&meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// database.file must exist for LoadDir validation of path safety only (file presence not required for safeJoin)
	if err := os.MkdirAll(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres", "data.tar.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := backup.Delete(p, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dry-run removed backup: %v", err)
	}

	if err := backup.Delete(p, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected removed, got %v", err)
	}
}

func TestDeleteMissing(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	err := backup.Delete(p, "qb-missing", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.BackupRestore {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.BackupRestore)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
