package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

// MaxAge is when a secret is reported as due for rotation.
const MaxAge = 180 * 24 * time.Hour

// Secret describes one credential in secrets.env.
type Secret struct {
	Name         string   `json:"name" yaml:"name"`
	Key          string   `json:"key" yaml:"key"`
	Desc         string   `json:"description" yaml:"description"`
	Bytes        int      `json:"bytes,omitempty" yaml:"bytes,omitempty"`
	EnvKeys      []string `json:"envKeys,omitempty" yaml:"envKeys,omitempty"`
	Placeholders []string `json:"placeholders,omitempty" yaml:"placeholders,omitempty"`
	Services     []string `json:"services,omitempty" yaml:"services,omitempty"`
	Files        []string `json:"files,omitempty" yaml:"files,omitempty"`
	Delivery     string   `json:"delivery,omitempty" yaml:"delivery,omitempty"`
	Mount        string   `json:"mount,omitempty" yaml:"mount,omitempty"`
	// Impact is what operators and users notice when it is rotated.
	Impact string `json:"impact" yaml:"impact"`
	// Destructive secrets make stored data unreadable when rotated and need --force.
	Destructive bool `json:"destructive,omitempty" yaml:"destructive,omitempty"`
}

var Catalog = []Secret{
	{Name: "postgres", Key: "POSTGRES_PASSWORD", Desc: "PostgreSQL superuser password", Impact: "API, workers and migrations restart (seconds)"},
	{Name: "postgres-app", Key: "POSTGRES_APP_PASSWORD", Desc: "PostgreSQL runtime role password", Impact: "API and workers restart (seconds)"},
	{Name: "redis", Key: "REDIS_PASSWORD", Desc: "Redis password", Impact: "API and workers restart; queued jobs are kept"},
	{Name: "minio", Key: "MINIO_ROOT_PASSWORD", Desc: "MinIO root password", Impact: "API and workers restart"},
	{Name: "jwt", Key: "JWT_SECRET", Desc: "signs user sessions", Impact: "every user is signed out"},
	{Name: "2fa", Key: "TWO_FACTOR_SECRET_ENCRYPTION_KEY", Desc: "encrypts stored 2FA secrets",
		Impact: "existing 2FA enrolments can no longer be verified; users must re-enrol", Destructive: true},
	{Name: "pepper", Key: "SERVICE_ACCOUNT_API_KEY_PEPPER", Desc: "hashes service-account API keys",
		Impact: "every service-account API key stops working and must be reissued", Destructive: true},
}

// Lookup finds a catalog entry by name.
func Lookup(name string) (Secret, bool) {
	for _, s := range Catalog {
		if s.Name == name {
			return s, true
		}
	}
	return Secret{}, false
}

// Value returns the bundle field for a catalog key.
func (b Bundle) Value(key string) string {
	switch key {
	case "POSTGRES_PASSWORD":
		return b.PostgresPassword
	case "POSTGRES_APP_PASSWORD":
		return b.PostgresAppPassword
	case "REDIS_PASSWORD":
		return b.RedisPassword
	case "MINIO_ROOT_USER":
		return b.MinioUser
	case "MINIO_ROOT_PASSWORD":
		return b.MinioPassword
	case "JWT_SECRET":
		return b.JWTSecret
	case "TWO_FACTOR_SECRET_ENCRYPTION_KEY":
		return b.TwoFactorKey
	case "SERVICE_ACCOUNT_API_KEY_PEPPER":
		return b.ServiceAccountAPIKeyPepper
	}
	return b.Extra[key]
}

// With returns a copy of b with key set to v.
func (b Bundle) With(key, v string) Bundle {
	switch key {
	case "POSTGRES_PASSWORD":
		b.PostgresPassword = v
	case "POSTGRES_APP_PASSWORD":
		b.PostgresAppPassword = v
	case "REDIS_PASSWORD":
		b.RedisPassword = v
	case "MINIO_ROOT_USER":
		b.MinioUser = v
	case "MINIO_ROOT_PASSWORD":
		b.MinioPassword = v
	case "JWT_SECRET":
		b.JWTSecret = v
	case "TWO_FACTOR_SECRET_ENCRYPTION_KEY":
		b.TwoFactorKey = v
	case "SERVICE_ACCOUNT_API_KEY_PEPPER":
		b.ServiceAccountAPIKeyPepper = v
	default:
		if b.Extra == nil {
			b.Extra = map[string]string{}
		}
		copy := make(map[string]string, len(b.Extra)+1)
		for k, value := range b.Extra {
			copy[k] = value
		}
		copy[key] = v
		b.Extra = copy
	}
	return b
}

// Stamp records when a secret was created and last rotated.
type Stamp struct {
	CreatedAt time.Time `json:"createdAt"`
	RotatedAt time.Time `json:"rotatedAt,omitempty"`
	// Backfilled marks CreatedAt as estimated from the file mtime.
	Backfilled bool `json:"backfilled,omitempty"`
}

// Since is the last time the value changed.
func (s Stamp) Since() time.Time {
	if s.RotatedAt.After(s.CreatedAt) {
		return s.RotatedAt
	}
	return s.CreatedAt
}

// Meta maps secret names to their stamps; stored in secrets.meta.json (0600).
type Meta map[string]Stamp

func MetaFile(p paths.Paths) string { return filepath.Join(p.ConfigDir, "secrets.meta.json") }

// LoadMeta reads secrets.meta.json; missing or unreadable files yield an empty Meta.
func LoadMeta(p paths.Paths) Meta {
	m := Meta{}
	if raw, err := os.ReadFile(MetaFile(p)); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// Backfill stamps name from file's mtime when it has no record yet.
// It reports whether m changed.
func (m Meta) Backfill(name, file string) bool {
	if _, ok := m[name]; ok {
		return false
	}
	st, err := os.Stat(file)
	if err != nil {
		return false
	}
	m[name] = Stamp{CreatedAt: st.ModTime().UTC(), Backfilled: true}
	return true
}

// Rotated records a rotation of name at t.
func (m Meta) Rotated(name string, t time.Time) {
	s := m[name]
	if s.CreatedAt.IsZero() {
		s.CreatedAt = t.UTC()
	}
	s.RotatedAt = t.UTC()
	m[name] = s
}

func SaveMeta(p paths.Paths, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return err
	}
	tmp := MetaFile(p) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, MetaFile(p))
}
