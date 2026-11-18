package secrets

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

// RotateDeps are the side effects of a rotation. Nil funcs are skipped.
type RotateDeps struct {
	Now func() time.Time
	// Backup takes a safety backup before anything changes.
	Backup func() (id string, err error)
	// Maintenance turns the maintenance page on/off around the change.
	Maintenance func(on bool) error
	// SetPostgresPassword changes the live database role password.
	SetPostgresPassword    func(password string) error
	SetPostgresAppPassword func(password string) error
	// Apply recreates containers so they pick up the new env files.
	Apply func() error
	// Verify waits until the stack is healthy again.
	Verify func() error
	// Generate returns a new value of n random bytes (hex encoded); defaults to crypto/rand.
	Generate func(n int) (string, error)
}

type RotateResult struct {
	Rotated    []string `json:"rotated"`
	Backup     string   `json:"backup,omitempty"`
	Files      []string `json:"files"`
	RolledBack bool     `json:"rolledBack,omitempty"`
}

// EnvFiles are the files that may carry secret values: secrets.env first.
func EnvFiles(p paths.Paths) []string {
	files := []string{p.SecretsFile()}
	matches, _ := filepath.Glob(filepath.Join(p.ConfigDir, ".env*"))
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			files = append(files, path)
		}
	}
	return files
}

// valueBytes is the random length used when generating a key.
func valueBytes(key string) int {
	switch key {
	case "POSTGRES_PASSWORD", "POSTGRES_APP_PASSWORD", "REDIS_PASSWORD", "MINIO_ROOT_PASSWORD":
		return 16
	}
	return 32
}

// Rotate replaces the named catalog secrets everywhere they are used, then
// applies and verifies the change. Any failure after files change restores
// the previous files (and database password) and re-applies them.
func Rotate(p paths.Paths, names []string, d RotateDeps) (RotateResult, error) {
	res := RotateResult{}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	gen := randomHex
	if d.Generate != nil {
		gen = d.Generate
	}
	raw, err := os.ReadFile(p.SecretsFile())
	if err != nil {
		return res, fmt.Errorf("read %s: %w", p.SecretsFile(), err)
	}
	catalog, err := CatalogFor(p)
	if err != nil {
		return res, err
	}
	old := parse(string(raw))
	next := old
	replace := map[string]string{}
	for _, name := range names {
		sec, ok := lookupIn(catalog, name)
		if !ok {
			return res, fmt.Errorf("unknown secret %q", name)
		}
		cur := old.Value(sec.Key)
		if len(cur) < 12 {
			return res, fmt.Errorf("%s is missing or too short in %s; refusing to rotate", sec.Key, p.SecretsFile())
		}
		v, err := gen(secretBytes(sec))
		if err != nil {
			return res, err
		}
		next = next.With(sec.Key, v)
		replace[sec.Key] = v
		for _, key := range sec.EnvKeys {
			replace[key] = v
		}
		res.Rotated = append(res.Rotated, name)
	}
	next.Register()

	snap := map[string][]byte{}
	for _, f := range EnvFiles(p) {
		if b, err := os.ReadFile(f); err == nil {
			snap[f] = b
		}
	}
	for _, name := range names {
		sec, _ := lookupIn(catalog, name)
		if sec.Delivery != "file" {
			continue
		}
		path := SecretValueFile(p, sec.Key)
		value, err := os.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("read mounted secret %s: %w", sec.Name, err)
		}
		if string(value) != old.Value(sec.Key) {
			return res, fmt.Errorf("mounted secret %s differs from secrets.env", sec.Name)
		}
		snap[path] = value
	}

	if d.Backup != nil {
		id, err := d.Backup()
		if err != nil {
			return res, fmt.Errorf("safety backup failed; nothing changed: %w", err)
		}
		res.Backup = id
	}
	if d.Maintenance != nil {
		if err := d.Maintenance(true); err != nil {
			return res, fmt.Errorf("enable maintenance; nothing changed: %w", err)
		}
		defer func() { _ = d.Maintenance(false) }()
	}

	pgChanged := false
	appPgChanged := false
	rollback := func(cause error) (RotateResult, error) {
		var errs []error
		for f, b := range snap {
			if err := writeAtomic(f, b); err != nil {
				errs = append(errs, err)
			}
		}
		if pgChanged && d.SetPostgresPassword != nil {
			if err := d.SetPostgresPassword(old.PostgresPassword); err != nil {
				errs = append(errs, fmt.Errorf("restore database password: %w", err))
			}
		}
		if appPgChanged && d.SetPostgresAppPassword != nil {
			if err := d.SetPostgresAppPassword(old.PostgresAppPassword); err != nil {
				errs = append(errs, fmt.Errorf("restore runtime database password: %w", err))
			}
		}
		if d.Apply != nil {
			if err := d.Apply(); err != nil {
				errs = append(errs, fmt.Errorf("re-apply previous secrets: %w", err))
			}
		}
		res.RolledBack = len(errs) == 0
		if len(errs) > 0 {
			return res, fmt.Errorf("%w; rollback incomplete: %v", cause, errors.Join(errs...))
		}
		return res, fmt.Errorf("%w; previous secrets restored", cause)
	}

	if _, ok := replace["POSTGRES_PASSWORD"]; ok && d.SetPostgresPassword != nil {
		if err := d.SetPostgresPassword(next.PostgresPassword); err != nil {
			return res, fmt.Errorf("change database password; nothing changed: %w", err)
		}
		pgChanged = true
	}
	if _, ok := replace["POSTGRES_APP_PASSWORD"]; ok && d.SetPostgresAppPassword != nil {
		if err := d.SetPostgresAppPassword(next.PostgresAppPassword); err != nil {
			return rollback(fmt.Errorf("change runtime database password: %w", err))
		}
		appPgChanged = true
	}
	for _, f := range EnvFiles(p) {
		b, ok := snap[f]
		if !ok {
			continue
		}
		fileReplace := map[string]string{}
		for _, name := range names {
			sec, _ := lookupIn(catalog, name)
			if f != p.SecretsFile() && !secretUsesFile(sec, f) {
				continue
			}
			fileReplace[sec.Key] = replace[sec.Key]
			for _, key := range sec.EnvKeys {
				fileReplace[key] = replace[sec.Key]
			}
		}
		body := replaceEnvValues(string(b), fileReplace, old)
		if body == string(b) {
			continue
		}
		if err := writeAtomic(f, []byte(body)); err != nil {
			return rollback(fmt.Errorf("write %s: %w", f, err))
		}
		res.Files = append(res.Files, f)
	}
	for _, name := range names {
		sec, _ := lookupIn(catalog, name)
		if sec.Delivery != "file" {
			continue
		}
		path := SecretValueFile(p, sec.Key)
		if err := writeAtomic(path, []byte(replace[sec.Key])); err != nil {
			return rollback(fmt.Errorf("write mounted secret %s: %w", sec.Name, err))
		}
		res.Files = append(res.Files, path)
	}
	if d.Apply != nil {
		if err := d.Apply(); err != nil {
			return rollback(fmt.Errorf("apply: %w", err))
		}
	}
	if d.Verify != nil {
		if err := d.Verify(); err != nil {
			return rollback(fmt.Errorf("verify: %w", err))
		}
	}

	meta := LoadMeta(p)
	for _, name := range res.Rotated {
		meta.Rotated(name, now())
	}
	_ = SaveMeta(p, meta)
	return res, nil
}

// replaceEnvValues updates assignments by key and any derived URI that embeds a
// rotated credential. It never substitutes a matching value in unrelated text.
func replaceEnvValues(body string, values map[string]string, old Bundle) string {
	lines := strings.SplitAfter(body, "\n")
	for i, line := range lines {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(key), "#") {
			continue
		}
		if next, ok := values[key]; ok {
			ending := ""
			if strings.HasSuffix(line, "\n") {
				ending = "\n"
			}
			lines[i] = key + "=" + next + ending
			continue
		}
		// Preserve URI encodings while changing only known credential fields.
		switch key {
		case "POSTGRES_URI", "DATABASE_URL":
			value = rotatedURLPassword(value, old.PostgresAppPassword, values["POSTGRES_APP_PASSWORD"])
			value = rotatedURLPassword(value, old.PostgresPassword, values["POSTGRES_PASSWORD"])
		case "REDIS_URL":
			value = rotatedURLPassword(value, old.RedisPassword, values["REDIS_PASSWORD"])
		case "STORAGE_SECRET_ACCESS_KEY":
			if next, ok := values["MINIO_ROOT_PASSWORD"]; ok && value == old.MinioPassword {
				value = next
			}
		}
		lines[i] = key + "=" + value
		if strings.HasSuffix(line, "\n") {
			lines[i] += "\n"
		}
	}
	return strings.Join(lines, "")
}

func rotatedURLPassword(raw, old, next string) string {
	if old == "" || next == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	password, ok := u.User.Password()
	if !ok || password != old {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), next)
	return u.String()
}

// writeAtomic replaces path keeping its mode (0600 when new).
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ResolveNames expands "all" to every non-destructive secret and validates names.
// Destructive secrets require force.
func ResolveNames(args []string, force bool) ([]string, error) {
	return resolveNames(Catalog, args, force)
}

func ResolveNamesFor(p paths.Paths, args []string, force bool) ([]string, error) {
	catalog, err := CatalogFor(p)
	if err != nil {
		return nil, err
	}
	return resolveNames(catalog, args, force)
}

func resolveNames(catalog []Secret, args []string, force bool) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, a := range args {
		if a == "all" {
			for _, s := range catalog {
				if !s.Destructive && !seen[s.Name] {
					seen[s.Name] = true
					out = append(out, s.Name)
				}
			}
			continue
		}
		s, ok := lookupIn(catalog, a)
		if !ok {
			return nil, fmt.Errorf("unknown secret %q", a)
		}
		if s.Destructive && !force {
			return nil, fmt.Errorf("rotating %s is destructive: %s (re-run with --force)", s.Name, s.Impact)
		}
		if !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s.Name)
		}
	}
	return out, nil
}
