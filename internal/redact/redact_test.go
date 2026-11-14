package redact

import (
	"strings"
	"testing"
)

func TestStringMasksPatternsAndRegisteredValues(t *testing.T) {
	Reset()
	Register("s3cr3t-minio-key")
	cases := map[string]string{
		"POSTGRES_PASSWORD=hunter22":                                "POSTGRES_PASSWORD=" + Mask,
		"JWT_SECRET: abc.def.ghi":                                   "JWT_SECRET: " + Mask,
		"PrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=": "PrivateKey = " + Mask,
		"postgresql://qabas_replica:pw123456@10.66.0.1:5432/db":     "postgresql://qabas_replica:" + Mask + "@10.66.0.1:5432/db",
		"host=10.66.0.1 user=qabas_replica password=abcdef":         "host=10.66.0.1 user=qabas_replica password=" + Mask,
		"mc alias set src http://x AKIA s3cr3t-minio-key":           "mc alias set src http://x AKIA " + Mask,
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStringMasksTokensAndFlags(t *testing.T) {
	Reset()
	cases := map[string]string{
		"auth set github-token ghp_abcdefghijklmnopqrstuvwxyz0123":        "auth set github-token " + Mask,
		"Authorization: Bearer abcdefgh12345678":                          "Authorization: Bearer " + Mask,
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlX19f": "token " + Mask,
		"qabas auth login --github-token s3cretvalue --yes":               "qabas auth login --github-token " + Mask + " --yes",
		"qabas backup create --name nightly":                              "qabas backup create --name nightly",
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortValuesAreNotRegistered(t *testing.T) {
	Reset()
	Register("abc")
	if got := String("abc def"); strings.Contains(got, Mask) {
		t.Fatalf("short value masked: %q", got)
	}
}
