//go:build !windows

package dr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

func TestPeerProbeExecutesNestedShell(t *testing.T) {
	d := t.TempDir()
	docker := `#!/bin/sh
test "$1" = exec && test "$2" = -i && test "$3" = qabasone-postgres || exit 41
shift 3
exec "$@"
`
	psql := `#!/bin/sh
test "$1" = -X && test "$2" = -At || exit 42
test "$3" = 'host=10.66.0.2 port=5432 dbname=postgres user=qabas_replica sslmode=require gssencmode=disable connect_timeout=2' || exit 43
test "$4" = -c && test "$5" = 'SELECT pg_is_in_recovery(), (SELECT system_identifier FROM pg_control_system())' || exit 44
test "$PGPASSWORD" = secret-password || exit 45
printf 'f|73\n'
`
	for name, body := range map[string]string{"docker": docker, "psql": psql} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	if !probeWritable(context.Background(), &nodeexec.Local{Name: "hq"}, "10.66.0.2", "secret-password", "73") {
		t.Fatal("nested shell did not pass conninfo, SQL and stdin password correctly")
	}
}
