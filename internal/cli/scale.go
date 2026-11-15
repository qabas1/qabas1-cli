package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/spf13/cobra"
)

const scaleMin = 2
const scaleMax = 4

func scaleCountFile(e *env) string { return filepath.Join(e.Paths.ConfigDir, "api-replicas.env") }

func scaleCount(e *env) int {
	b, err := os.ReadFile(scaleCountFile(e))
	if err != nil {
		return scaleMin
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(b), "QABAS_API_REPLICAS=")))
	if err != nil || n < scaleMin || n > scaleMax {
		return scaleMin
	}
	return n
}

func scaleEnvironment(e *env, version string, count int) []string {
	env := compose.EnvForInstalledRelease(e.Paths, version)
	return append(env, fmt.Sprintf("QABAS_API_REPLICAS=%d", count))
}

func ensureScaleOverlay(e *env) error {
	path := filepath.Join(e.Paths.ComposeDir(), nodestack.OverlayScale)
	if _, err := os.Stat(path); err != nil {
		cfg, err := config.Load(e.Paths)
		if err != nil {
			return err
		}
		data, err := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref).FetchComposeFile(nodestack.OverlayScale)
		if err != nil {
			return fmt.Errorf("fetch scale overlay: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nodestack.AddDirective(e.Paths.ComposeDir(), "file:"+nodestack.OverlayScale)
}

func setAPIScale(e *env, count int) error {
	if count < scaleMin || count > scaleMax {
		return fmt.Errorf("API replicas must be between %d and %d", scaleMin, scaleMax)
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		return err
	}
	if !st.Installed() {
		return fmt.Errorf("QabasOne is not installed")
	}
	_, writer, err := e.scheduleCluster()
	if err != nil {
		return err
	}
	if !writer {
		return fmt.Errorf("API autoscaling is available only on the writer")
	}
	if e.flags.DryRun {
		e.Out.Printf("would set API replicas to %d\n", count)
		return nil
	}
	if err := checkAPIRouting(e); err != nil {
		return err
	}
	if err := ensureScaleOverlay(e); err != nil {
		return err
	}
	rt := newRuntime()
	serviceEnv := scaleEnvironment(e, st.CurrentRelease, count)
	if err := rt.UpService(e.Paths, serviceEnv, "pgbouncer", false, true); err != nil {
		return err
	}
	if err := rt.ScaleAPI(e.Paths, serviceEnv, count); err != nil {
		return err
	}
	if err := os.MkdirAll(e.Paths.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(scaleCountFile(e), []byte("QABAS_API_REPLICAS="+strconv.Itoa(count)+"\n"), 0o644); err != nil {
		return err
	}
	return nodestack.AddDirective(e.Paths.ComposeDir(), "env-file:"+scaleCountFile(e))
}

// The managed gateway refreshes API container addresses via Docker DNS.
func checkAPIRouting(e *env) error {
	if _, err := os.Stat(filepath.Join(e.Paths.ComposeDir(), "compose.gateway.yaml")); err != nil {
		return fmt.Errorf("run 'qabas gateway apply' before API scaling")
	}
	id, err := installedGatewayContainerID(e)
	if err != nil {
		return err
	}
	out, err := exec.Command("docker", "exec", id, "nginx", "-T", "-c", "/etc/qabas-gateway/nginx.conf").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot verify QabasOne gateway routing: %w", err)
	}
	conf := string(out)
	if !strings.Contains(conf, "resolver 127.0.0.11") || !strings.Contains(conf, "api:3030 resolve") {
		return fmt.Errorf("QabasOne gateway must resolve API replicas through Docker DNS")
	}
	return nil
}

func newScaleCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{Use: "scale", Short: "Set service capacity", RunE: parentHelp()}
	cmd.AddCommand(&cobra.Command{Use: "api REPLICAS", Short: "Run 2 to 4 API replicas", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid replica count %q", args[0])
		}
		e := load(f)
		return e.withLock(func() error { return setAPIScale(e, n) })
	}})
	return cmd
}
