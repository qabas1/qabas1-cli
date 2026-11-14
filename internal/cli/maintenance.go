package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/spf13/cobra"
)

const (
	defaultEdgeNginxDir       = "/srv/projects/edge/nginx"
	defaultEdgeNginxContainer = "edge-nginx"
	maintenanceIncludeURI     = "/etc/nginx/includes/qabasone-maintenance.conf"
	maintenanceFlagURI        = "/usr/share/nginx/html/qabasone-maintenance.enabled"
	maintenanceIncludeLine    = "include /etc/nginx/includes/qabasone-maintenance.conf"
	maintenanceIncludesGlob   = "include /etc/nginx/includes/*.conf"
)

// Injected for tests.
var (
	edgeDockerLookPath    = exec.LookPath
	edgeInspectMountsJSON = func(container string) ([]byte, error) {
		cmd := exec.Command("docker", "inspect", container, "--format", "{{json .Mounts}}")
		return cmd.Output()
	}
	edgeReloadNginx = func(container string) error {
		cmd := exec.Command("docker", "exec", container, "nginx", "-s", "reload")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return exitcode.New(exitcode.Network, "reload %s: %s", container, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
)

type edgeLayout struct {
	HTMLDir     string
	IncludesDir string
	ConfDDir    string
	Container   string
	IncludeURI  string
	FlagURI     string
}

func newMaintenanceCmd(f *flags) *cobra.Command {
	var untilText string
	var forDuration time.Duration
	var pagePath string
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Control maintenance mode",
		Example: `  sudo qabas maintenance on --for 30m
  sudo qabas maintenance on --until 2026-08-30T18:00:00Z
  sudo qabas maintenance on --page /etc/qabasone/maintenance.html
  sudo qabas maintenance off
  qabas maintenance status`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	set := func(on bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				st, err := state.Load(e.Paths)
				if err != nil {
					return err
				}
				st.Maintenance = on
				if on {
					until, err := maintenanceUntil(untilText, forDuration)
					if err != nil {
						return err
					}
					st.MaintenanceUntil = until
				} else {
					st.MaintenanceUntil = time.Time{}
				}
				if f.DryRun {
					e.Out.Printf("would set maintenance=%v\n", on)
					return nil
				}
				if err := setMaintenanceMode(e.Paths, &st, on, st.MaintenanceUntil, pagePath); err != nil {
					return err
				}
				if on {
					e.Out.Printf("maintenance mode on until %s\n", st.MaintenanceUntil.Local().Format("2006-01-02 15:04 MST"))
				} else {
					e.Out.Println("maintenance mode off")
				}
				return nil
			})
		}
	}
	cmd.AddCommand(
		&cobra.Command{Use: "on", Short: "Enter maintenance mode", RunE: set(true)},
		&cobra.Command{Use: "off", Short: "Leave maintenance mode", RunE: set(false)},
		&cobra.Command{Use: "status", Short: "Show maintenance mode", RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := state.Load(e.Paths)
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			if st.Maintenance {
				if st.MaintenanceUntil.IsZero() {
					fmt.Fprintln(cmd.OutOrStdout(), "on")
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "on until %s\n", st.MaintenanceUntil.Local().Format("2006-01-02 15:04 MST"))
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "off")
			}
			return nil
		}},
	)
	cmd.PersistentFlags().DurationVar(&forDuration, "for", 30*time.Minute, "expected maintenance duration")
	cmd.PersistentFlags().StringVar(&untilText, "until", "", "expected return time (RFC3339 or local YYYY-MM-DD HH:MM)")
	cmd.PersistentFlags().StringVar(&pagePath, "page", "", "HTML template for the maintenance page (overrides QABAS_MAINTENANCE_PAGE)")
	return cmd
}

func setMaintenanceMode(p paths.Paths, st *state.State, on bool, until time.Time, pagePath string) error {
	if on {
		if err := enableEdgeMaintenance(until, pagePath); err != nil {
			return err
		}
		st.Maintenance = true
		st.MaintenanceUntil = until
	} else {
		if err := disableEdgeMaintenance(); err != nil {
			return err
		}
		st.Maintenance = false
		st.MaintenanceUntil = time.Time{}
	}
	return state.Save(p, *st)
}

func maintenanceUntil(raw string, d time.Duration) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		if d <= 0 {
			d = 30 * time.Minute
		}
		return time.Now().Add(d).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02 15:04 MST"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, exitcode.NewHint(exitcode.InvalidInput,
		"use --for 30m or --until 2026-08-30T18:00:00Z",
		"invalid maintenance return time")
}

func edgeNginxContainer() string {
	if v := strings.TrimSpace(os.Getenv("QABAS_EDGE_NGINX_CONTAINER")); v != "" {
		return v
	}
	return defaultEdgeNginxContainer
}

func layoutFromRoot(root, container string) edgeLayout {
	return edgeLayout{
		HTMLDir:     filepath.Join(root, "html"),
		IncludesDir: filepath.Join(root, "includes"),
		ConfDDir:    filepath.Join(root, "conf.d"),
		Container:   container,
		IncludeURI:  maintenanceIncludeURI,
		FlagURI:     maintenanceFlagURI,
	}
}

func resolveEdgeLayout() (edgeLayout, error) {
	container := edgeNginxContainer()
	if root := strings.TrimSpace(os.Getenv("QABAS_EDGE_NGINX_DIR")); root != "" {
		return layoutFromRoot(root, container), nil
	}
	if layout, ok, err := layoutFromDockerMounts(container); err != nil {
		return edgeLayout{}, err
	} else if ok {
		return layout, nil
	}
	if st, err := os.Stat(defaultEdgeNginxDir); err == nil && st.IsDir() {
		return layoutFromRoot(defaultEdgeNginxDir, container), nil
	}
	return edgeLayout{}, exitcode.NewHint(exitcode.InvalidInput,
		"set QABAS_EDGE_NGINX_DIR to the host nginx root (html/, includes/, conf.d/), or ensure container "+container+" is running with those mounts",
		"%w", errEdgeLayoutNotFound)
}

var errEdgeLayoutNotFound = errors.New("edge nginx layout not found")

// hostMountSource maps Docker Desktop VM mount paths back to Windows host
// paths (/run/desktop/mnt/host/c/x or /host_mnt/c/x -> C:\x).
func hostMountSource(goos, src string) string {
	if goos != "windows" {
		return src
	}
	for _, prefix := range []string{"/run/desktop/mnt/host/", "/host_mnt/"} {
		rest, ok := strings.CutPrefix(src, prefix)
		if !ok || rest == "" {
			continue
		}
		drive, tail, _ := strings.Cut(rest, "/")
		if len(drive) != 1 {
			return src
		}
		return strings.ToUpper(drive) + `:\` + strings.ReplaceAll(tail, "/", `\`)
	}
	return src
}

type dockerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

func layoutFromDockerMounts(container string) (edgeLayout, bool, error) {
	if _, err := edgeDockerLookPath("docker"); err != nil {
		return edgeLayout{}, false, nil
	}
	out, err := edgeInspectMountsJSON(container)
	if err != nil {
		return edgeLayout{}, false, nil
	}
	var mounts []dockerMount
	if err := json.Unmarshal(out, &mounts); err != nil {
		return edgeLayout{}, false, exitcode.New(exitcode.Generic, "parse docker mounts for %s: %w", container, err)
	}
	layout := edgeLayout{
		Container:  container,
		IncludeURI: maintenanceIncludeURI,
		FlagURI:    maintenanceFlagURI,
	}
	var nginxRoot string
	for _, m := range mounts {
		src := hostMountSource(runtime.GOOS, m.Source)
		if src == "" {
			continue
		}
		switch strings.TrimRight(m.Destination, "/") {
		case "/usr/share/nginx/html":
			layout.HTMLDir = src
		case "/etc/nginx/includes":
			layout.IncludesDir = src
		case "/etc/nginx/conf.d":
			layout.ConfDDir = src
		case "/etc/nginx":
			nginxRoot = src
		}
	}
	if nginxRoot != "" {
		if layout.ConfDDir == "" {
			layout.ConfDDir = filepath.Join(nginxRoot, "conf.d")
		}
		if layout.IncludesDir == "" {
			layout.IncludesDir = filepath.Join(nginxRoot, "includes")
		}
	}
	if layout.HTMLDir == "" || layout.IncludesDir == "" || layout.ConfDDir == "" {
		return edgeLayout{}, false, nil
	}
	return layout, true, nil
}

func resolveMaintenancePagePath(flagPath string) string {
	if v := strings.TrimSpace(flagPath); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("QABAS_MAINTENANCE_PAGE"))
}

func enableEdgeMaintenance(until time.Time, pageFlag string) error {
	layout, err := resolveEdgeLayout()
	if err != nil {
		return err
	}
	if err := verifyMaintenanceInclude(layout); err != nil {
		return err
	}
	html, err := maintenanceHTML(until, resolveMaintenancePagePath(pageFlag))
	if err != nil {
		return err
	}
	if err := writeMaintenancePage(layout, html); err != nil {
		return err
	}
	if err := writeMaintenanceInclude(layout, until, html); err != nil {
		return err
	}
	flag := filepath.Join(layout.HTMLDir, "qabasone-maintenance.enabled")
	if err := os.MkdirAll(layout.HTMLDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(flag, []byte(until.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	return reloadEdgeNginx(layout.Container)
}

func disableEdgeMaintenance() error {
	layout, err := resolveEdgeLayout()
	if err != nil {
		return err
	}
	flag := filepath.Join(layout.HTMLDir, "qabasone-maintenance.enabled")
	if err := os.Remove(flag); err != nil && !os.IsNotExist(err) {
		return err
	}
	return reloadEdgeNginx(layout.Container)
}

func writeMaintenancePage(layout edgeLayout, html string) error {
	if err := os.MkdirAll(layout.HTMLDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(layout.HTMLDir, "qabasone-maintenance.html"), []byte(html), 0o644)
}

func writeMaintenanceInclude(layout edgeLayout, until time.Time, html string) error {
	if err := os.MkdirAll(layout.IncludesDir, 0o755); err != nil {
		return err
	}
	retryAfter := int(time.Until(until).Seconds())
	if retryAfter < 60 {
		retryAfter = 60
	}
	body := fmt.Sprintf(`default_type text/html;
add_header Retry-After %d always;
add_header Cache-Control "no-store" always;
if (-f %s) {
    return 503 %s;
}
`, retryAfter, layout.FlagURI, strconv.Quote(compactHTML(html)))
	return os.WriteFile(filepath.Join(layout.IncludesDir, "qabasone-maintenance.conf"), []byte(body), 0o644)
}

type maintenancePageData struct {
	Until string
}

func maintenanceHTML(until time.Time, pagePath string) (string, error) {
	untilLabel := until.Local().Format("Monday, 02 Jan 2006 15:04 MST")
	data := maintenancePageData{Until: untilLabel}
	if pagePath == "" {
		return renderMaintenanceTemplate(defaultMaintenancePage, data)
	}
	b, err := os.ReadFile(pagePath)
	if err != nil {
		return "", exitcode.NewHint(exitcode.InvalidInput,
			"set --page or QABAS_MAINTENANCE_PAGE to a readable HTML template",
			"read maintenance page %s: %v", pagePath, err)
	}
	return renderMaintenanceTemplate(string(b), data)
}

func renderMaintenanceTemplate(src string, data maintenancePageData) (string, error) {
	tmpl, err := template.New("maintenance").Parse(src)
	if err != nil {
		return "", exitcode.New(exitcode.InvalidInput, "parse maintenance page template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", exitcode.New(exitcode.InvalidInput, "render maintenance page template: %w", err)
	}
	return buf.String(), nil
}

const defaultMaintenancePage = `<!doctype html>
<html lang="ar" dir="rtl">

<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="robots" content="noindex">
    <title>صيانة قبس ون</title>

    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Sans+Arabic:wght@400;700&display=swap"
        rel="stylesheet">

    <style>
        :root {
            color-scheme: light dark;
            font-family: "IBM Plex Sans Arabic", sans-serif;
            color: #17202a;
            background: #f7f8fa
        }

        * {
            box-sizing: border-box
        }

        body {
            margin: 0;
            min-height: 100vh;
            display: grid;
            place-items: center;
            padding: 24px;
            background: radial-gradient(circle at top right, #eef4ff, transparent 32rem), #f7f8fa;
            text-align: right
        }

        main {
            width: min(100%, 560px)
        }

        .panel {
            background: rgba(255, 255, 255, .86);
            border: 1px solid #d9e0e8;
            border-radius: 8px;
            padding: 32px;
            box-shadow: 0 24px 80px rgba(23, 32, 42, .08)
        }

        .eyebrow {
            margin: 0 0 14px;
            font-size: 13px;
            font-weight: 700;
            color: #526171
        }

        h1 {
            margin: 0 0 14px;
            font-size: clamp(28px, 5vw, 40px);
            line-height: 1.3
        }

        p {
            margin: 0;
            color: #465465;
            font-size: 16px;
            line-height: 1.8
        }

        .time {
            margin-top: 22px;
            padding-top: 20px;
            border-top: 1px solid #dde4ec
        }

        .label {
            display: block;
            margin-bottom: 4px;
            color: #657386;
            font-size: 13px
        }

        .value {
            color: #17202a;
            font-weight: 700
        }

        @media (prefers-color-scheme:dark) {
            :root {
                color: #edf2f7;
                background: #111827
            }

            body {
                background: radial-gradient(circle at top right, #1d3557, transparent 30rem), #111827
            }

            .panel {
                background: rgba(17, 24, 39, .9);
                border-color: #2c3a4d;
                box-shadow: none
            }

            .eyebrow,
            .label {
                color: #9ca9ba
            }

            p {
                color: #c2ccd8
            }

            .value {
                color: #f8fafc
            }

            .time {
                border-color: #304055
            }
        }
    </style>
</head>

<body>
    <main class="panel">
        <p class="eyebrow">صيانة مجدولة</p>
        <h1>تحديث سريع</h1>
        <p>نعمل على تحسين الخدمة وسنعود قريبًا. لا يلزمك اتخاذ أي إجراء.</p>
        <p class="time">
            <span class="label">موعد العودة المتوقع</span>
            <span class="value">خلال 5 دقائق</span>
        </p>
    </main>
</body>

</html>
`

func compactHTML(s string) string {
	lines := strings.Fields(s)
	return strings.Join(lines, " ")
}

func verifyMaintenanceInclude(layout edgeLayout) error {
	paths, err := maintenanceVerifyPaths(layout)
	if err != nil {
		return err
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if confHasMaintenanceInclude(string(b)) {
			return nil
		}
	}
	hint := "add this line inside each qabas server block (edge repo, not via the CLI): include " + layout.IncludeURI + ";"
	return exitcode.NewHint(exitcode.InvalidInput, hint,
		"edge nginx configs do not include the maintenance snippet")
}

func maintenanceVerifyPaths(layout edgeLayout) ([]string, error) {
	if conf := strings.TrimSpace(os.Getenv("QABAS_EDGE_NGINX_CONF")); conf != "" {
		st, err := os.Stat(conf)
		if err != nil {
			return nil, exitcode.NewHint(exitcode.InvalidInput,
				"set QABAS_EDGE_NGINX_CONF to an existing conf file or conf.d directory",
				"edge nginx conf path %s: %v", conf, err)
		}
		if st.IsDir() {
			return listConfFiles(conf)
		}
		return []string{conf}, nil
	}
	return listConfFiles(layout.ConfDDir)
}

func listConfFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".conf") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths, nil
}

func confHasMaintenanceInclude(s string) bool {
	return strings.Contains(s, maintenanceIncludeLine) || strings.Contains(s, maintenanceIncludesGlob)
}

func reloadEdgeNginx(container string) error {
	if _, err := edgeDockerLookPath("docker"); err != nil {
		return nil
	}
	if container == "" {
		container = defaultEdgeNginxContainer
	}
	return edgeReloadNginx(container)
}
