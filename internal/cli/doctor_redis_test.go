package cli

import "testing"

func TestRedisKeyFamilyMasksIdentifiers(t *testing.T) {
	cases := map[string]string{
		"dr:auth:login-fail:ahmed@example.com":         "dr:auth:login-fail",
		"dr:session:dr_9f8e7d6c5b4a39281706f5e4d3c2b1": "dr:session:*",
		"leader:operations-worker":                     "leader:operations-worker",
		"cache:tenant:12345:reports":                   "cache:tenant:*",
		"bull:notifications:987654321":                 "bull:notifications:*",
	}
	for in, want := range cases {
		if got := redisKeyFamily(in); got != want {
			t.Errorf("redisKeyFamily(%q) = %q, want %q", in, got, want)
		}
	}
}
