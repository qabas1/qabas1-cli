package cli

import (
	"strings"
	"testing"
)

func TestDefaultSeedCommandPrefersRealSeedOverBootstrapHelper(t *testing.T) {
	command := defaultSeedCommand(false)

	compiledSeedCLI := strings.Index(command, "[ -f dist/core/seed/seed.cli.js ]")
	if compiledSeedCLI < 0 {
		t.Fatal("default seed command does not check dist/core/seed/seed.cli.js")
	}

	realSeed := strings.Index(command, "[ -f prisma/seed.ts ]")
	bootstrapHelper := strings.Index(command, "[ -f prisma/seed-bootstrap.ts ]")
	if realSeed < 0 {
		t.Fatal("default seed command does not check prisma/seed.ts")
	}
	if bootstrapHelper < 0 {
		t.Fatal("default seed command does not check prisma/seed-bootstrap.ts")
	}
	if realSeed > bootstrapHelper {
		t.Fatal("default seed command must prefer prisma/seed.ts over prisma/seed-bootstrap.ts")
	}

	distRealSeed := strings.Index(command, "[ -f dist/prisma/seed.js ]")
	distBootstrapHelper := strings.Index(command, "[ -f dist/prisma/seed-bootstrap.js ]")
	if distRealSeed < 0 {
		t.Fatal("default seed command does not check dist/prisma/seed.js")
	}
	if distBootstrapHelper < 0 {
		t.Fatal("default seed command does not check dist/prisma/seed-bootstrap.js")
	}
	if compiledSeedCLI > distRealSeed {
		t.Fatal("default seed command must prefer production seed CLI over legacy dist/prisma/seed.js")
	}
	if distRealSeed > distBootstrapHelper {
		t.Fatal("default seed command must prefer dist/prisma/seed.js over dist/prisma/seed-bootstrap.js")
	}
}

func TestDefaultSeedCommandDoesNotRewriteSeedInternals(t *testing.T) {
	interactive := defaultSeedCommand(false)
	if strings.Contains(interactive, "SEED_CONFIRM=") {
		t.Fatalf("interactive seed must not force SEED_CONFIRM:\n%s", interactive)
	}
	if !strings.Contains(interactive, "node dist/core/seed/seed.cli.js") {
		t.Fatal("interactive seed missing seed.cli.js invocation")
	}

	auto := defaultSeedCommand(true)
	if !strings.Contains(auto, `export SEED_CONFIRM="${SEED_CONFIRM:-yes}"`) {
		t.Fatal("auto-confirm seed command missing SEED_CONFIRM export")
	}
	if !strings.Contains(auto, "node dist/core/seed/seed.cli.js") {
		t.Fatal("auto-confirm seed missing seed.cli.js invocation")
	}

	for _, removed := range []string{
		`prismaShimDist`,
		`"/dist/core/seed/"`,
		`"/dist/src/core/seed/"`,
		`..\/src\/core\/seed\/seed-bootstrap.ts`,
		`..\/src\/core\/seed\/seed-confirm.ts`,
		`..\/src\/core\/seed\/seed-modules.ts`,
	} {
		if strings.Contains(auto, removed) || strings.Contains(interactive, removed) {
			t.Fatalf("default seed command still contains fragile compiled shim lookup %q", removed)
		}
	}
}

func TestWithSeedAutoConfirmPropagatesYesToCustomCommand(t *testing.T) {
	custom := "node dist/core/seed/seed.cli.js --only=bootstrap"
	if got := withSeedAutoConfirm(custom, false); got != custom {
		t.Fatalf("interactive custom command should be unchanged, got %q", got)
	}

	got := withSeedAutoConfirm(custom, true)
	if !strings.HasPrefix(got, `export SEED_CONFIRM="${SEED_CONFIRM:-yes}"`) {
		t.Fatalf("auto-confirm custom command missing SEED_CONFIRM export:\n%s", got)
	}
	if !strings.Contains(got, custom) {
		t.Fatalf("auto-confirm custom command lost original command:\n%s", got)
	}
}
