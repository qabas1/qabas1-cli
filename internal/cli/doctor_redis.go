package cli

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/spf13/cobra"
)

// redisKeyFamily groups a key by its first two ":"-separated segments with any
// id-looking segment masked, so user identifiers never reach the terminal.
func redisKeyFamily(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) > 3 {
		parts = parts[:3]
	}
	for i, p := range parts {
		if looksLikeID(p) {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, ":")
}

func looksLikeID(s string) bool {
	if strings.Contains(s, "@") || strings.HasPrefix(s, "dr_") {
		return true
	}
	digits, hexish := 0, true
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-':
		default:
			hexish = false
		}
	}
	return digits > 0 && (digits*2 >= len(s) || (hexish && len(s) >= 8))
}

type redisAudit struct {
	Families  map[string]int `json:"families"`
	Total     int            `json:"total"`
	MaxMemory string         `json:"maxmemoryPolicy"`
	UsedBytes int64          `json:"usedMemoryBytes"`
	Notes     []string       `json:"notes,omitempty"`
}

// auditRedis scans key NAMES only (SCAN, never GET), plus memory settings.
func auditRedis(ctx context.Context, x nodeexec.Executor) (redisAudit, error) {
	a := redisAudit{Families: map[string]int{}}
	keys, err := nodeexec.Output(ctx, x, `docker exec qabasone-redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli --no-auth-warning --scan --count 1000'`)
	if err != nil {
		return a, err
	}
	for _, k := range strings.Split(keys, "\n") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		a.Total++
		a.Families[redisKeyFamily(k)]++
	}
	info, _ := nodeexec.Output(ctx, x, `docker exec qabasone-redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli --no-auth-warning CONFIG GET maxmemory-policy; REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli --no-auth-warning INFO memory'`)
	lines := strings.Split(info, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "maxmemory-policy" && i+1 < len(lines) {
			a.MaxMemory = strings.TrimSpace(lines[i+1])
		}
		if v, ok := strings.CutPrefix(l, "used_memory:"); ok {
			a.UsedBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if a.Families["dr:auth:login-fail:*"] > 0 || a.Families["dr:auth:otp-fail:*"] > 0 {
		a.Notes = append(a.Notes, "read-only-node login/OTP lockout counters present (expected on replicas)")
	}
	a.Notes = append(a.Notes, "Redis is node-local cache/session state: never replicated; a promoted node starts empty (users log in again)")
	return a, nil
}

func newDoctorRedisCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "redis",
		Short: "Audit Redis key families (names only, never values)",
		Long: `Scan Redis key names with SCAN and group them into families (ids masked). Values are
never read. Use it to confirm Redis only holds disposable cache/session/lock state that
is safe to lose on promotion.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			a, err := auditRedis(cmd.Context(), &nodeexec.Local{Name: "local"})
			if err != nil {
				return exitcode.Wrap(exitcode.Health, err)
			}
			if f.JSON {
				return e.Out.Encode(a)
			}
			fams := make([]string, 0, len(a.Families))
			for k := range a.Families {
				fams = append(fams, k)
			}
			sort.Slice(fams, func(i, j int) bool { return a.Families[fams[i]] > a.Families[fams[j]] })
			rows := [][]string{}
			for _, k := range fams {
				rows = append(rows, []string{k, strconv.Itoa(a.Families[k])})
			}
			if err := output.Table(cmd.OutOrStdout(), []string{"KEY FAMILY", "KEYS"}, rows); err != nil {
				return err
			}
			e.Out.Printf("total %d keys, used memory %d MiB, maxmemory-policy %s\n", a.Total, a.UsedBytes>>20, a.MaxMemory)
			for _, n := range a.Notes {
				e.Out.Printf("note: %s\n", n)
			}
			return nil
		},
	}
}
