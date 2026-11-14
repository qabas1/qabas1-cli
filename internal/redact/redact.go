// Package redact scrubs secrets from anything qabas prints or logs.
//
// Two layers: exact values registered at runtime (passwords, keys read from the
// per-node secret files) and pattern-based masking of KEY=VALUE / URI credentials /
// WireGuard PrivateKey lines, so a secret that was never registered still does not leak.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const Mask = "[REDACTED]"

var (
	mu     sync.RWMutex
	values = map[string]struct{}{}
)

var patterns = []*regexp.Regexp{
	// KEY=VALUE and KEY: VALUE where the key names a secret.
	regexp.MustCompile(`(?i)\b([A-Z0-9_]*(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_?KEY|ACCESS_?KEY|API_?KEY)[A-Z0-9_]*)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s"']+)`),
	// WireGuard config.
	regexp.MustCompile(`(?i)\b(PrivateKey|PresharedKey)(\s*=\s*)(\S+)`),
	// Credentials in URIs: scheme://user:pass@host
	regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^@\s/]+)(@)`),
	// libpq conninfo password=...
	regexp.MustCompile(`(?i)\b(password)(\s*=\s*)('[^']*'|\S+)`),
}

// tokenPatterns mask well-known token shapes wherever they appear.
var tokenPatterns = []*regexp.Regexp{
	// GitHub personal/OAuth/app tokens.
	regexp.MustCompile(`\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}`),
	// JWTs.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
}

var (
	bearerPattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)(\s+)([A-Za-z0-9._~+/=-]{8,})`)
	// Secret-looking CLI flags given as "--token VALUE".
	flagPattern = regexp.MustCompile(`(?i)(--[a-z0-9-]*(?:password|passwd|secret|token|key)[a-z0-9-]*)(\s+)([^\s-][^\s]*)`)
)

// Register adds exact secret values to scrub. Values shorter than 6 characters are
// ignored to avoid masking ordinary words.
func Register(secrets ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) >= 6 {
			values[s] = struct{}{}
		}
	}
}

// Reset clears registered values (tests).
func Reset() {
	mu.Lock()
	values = map[string]struct{}{}
	mu.Unlock()
}

// String returns s with all known and pattern-matched secrets masked.
func String(s string) string {
	if s == "" {
		return s
	}
	mu.RLock()
	known := make([]string, 0, len(values))
	for v := range values {
		known = append(known, v)
	}
	mu.RUnlock()
	// Longest first so a secret containing another secret is fully masked.
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	for _, v := range known {
		s = strings.ReplaceAll(s, v, Mask)
	}

	s = patterns[0].ReplaceAllString(s, "${1}${3}"+Mask)
	s = patterns[1].ReplaceAllString(s, "${1}${2}"+Mask)
	s = patterns[2].ReplaceAllString(s, "${1}"+Mask+"${3}")
	s = patterns[3].ReplaceAllString(s, "${1}${2}"+Mask)
	for _, re := range tokenPatterns {
		s = re.ReplaceAllString(s, Mask)
	}
	s = bearerPattern.ReplaceAllString(s, "${1}${2}"+Mask)
	s = flagPattern.ReplaceAllString(s, "${1}${2}"+Mask)
	return s
}

// Args returns a redacted, space-joined command line.
func Args(args []string) string { return String(strings.Join(args, " ")) }

// Error returns err's message redacted (nil-safe).
func Error(err error) string {
	if err == nil {
		return ""
	}
	return String(err.Error())
}
