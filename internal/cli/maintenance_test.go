package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/deploy"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/state"
)

func TestLayoutFromRoot(t *testing.T) {
	layout := layoutFromRoot("/srv/edge/nginx", "edge-nginx")
	if layout.HTMLDir != filepath.Join("/srv/edge/nginx", "html") {
		t.Fatalf("HTMLDir=%q", layout.HTMLDir)
	}
	if layout.IncludesDir != filepath.Join("/srv/edge/nginx", "includes") {
		t.Fatalf("IncludesDir=%q", layout.IncludesDir)
	}
	if layout.ConfDDir != filepath.Join("/srv/edge/nginx", "conf.d") {
		t.Fatalf("ConfDDir=%q", layout.ConfDDir)
	}
	if layout.IncludeURI != maintenanceIncludeURI || layout.FlagURI != maintenanceFlagURI {
		t.Fatalf("uris include=%q flag=%q", layout.IncludeURI, layout.FlagURI)
	}
}

func TestResolveEdgeLayoutFromEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QABAS_EDGE_NGINX_DIR", root)
	t.Setenv("QABAS_EDGE_NGINX_CONTAINER", "custom-edge")
	layout, err := resolveEdgeLayout()
	if err != nil {
		t.Fatal(err)
	}
	if layout.Container != "custom-edge" {
		t.Fatalf("container=%q", layout.Container)
	}
	if layout.HTMLDir != filepath.Join(root, "html") {
		t.Fatalf("HTMLDir=%q", layout.HTMLDir)
	}
}

func TestResolveEdgeLayoutMissing(t *testing.T) {
	t.Setenv("QABAS_EDGE_NGINX_DIR", "")
	t.Setenv("QABAS_EDGE_NGINX_CONTAINER", "missing-edge")
	origLook := edgeDockerLookPath
	origInspect := edgeInspectMountsJSON
	t.Cleanup(func() {
		edgeDockerLookPath = origLook
		edgeInspectMountsJSON = origInspect
	})
	edgeDockerLookPath = func(string) (string, error) { return "", errors.New("not found") }
	edgeInspectMountsJSON = func(string) ([]byte, error) {
		t.Fatal("inspect should not run when docker is missing")
		return nil, nil
	}
	if _, err := os.Stat(defaultEdgeNginxDir); err == nil {
		t.Skip("default edge nginx dir exists on this host")
	}
	_, err := resolveEdgeLayout()
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.HintOf(err) == "" {
		t.Fatalf("expected hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "edge nginx layout not found") {
		t.Fatalf("got %v", err)
	}
}

func TestLayoutFromDockerMounts(t *testing.T) {
	origLook := edgeDockerLookPath
	origInspect := edgeInspectMountsJSON
	t.Cleanup(func() {
		edgeDockerLookPath = origLook
		edgeInspectMountsJSON = origInspect
	})
	edgeDockerLookPath = func(string) (string, error) { return "/bin/docker", nil }
	mounts := []dockerMount{
		{Source: "/data/html", Destination: "/usr/share/nginx/html"},
		{Source: "/data/includes", Destination: "/etc/nginx/includes"},
		{Source: "/data/conf.d", Destination: "/etc/nginx/conf.d"},
	}
	raw, err := json.Marshal(mounts)
	if err != nil {
		t.Fatal(err)
	}
	edgeInspectMountsJSON = func(container string) ([]byte, error) {
		if container != "edge-nginx" {
			t.Fatalf("container=%q", container)
		}
		return raw, nil
	}

	layout, ok, err := layoutFromDockerMounts("edge-nginx")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if layout.HTMLDir != "/data/html" || layout.IncludesDir != "/data/includes" || layout.ConfDDir != "/data/conf.d" {
		t.Fatalf("%+v", layout)
	}
}

func TestLayoutFromDockerMountsNginxRoot(t *testing.T) {
	origLook := edgeDockerLookPath
	origInspect := edgeInspectMountsJSON
	t.Cleanup(func() {
		edgeDockerLookPath = origLook
		edgeInspectMountsJSON = origInspect
	})
	edgeDockerLookPath = func(string) (string, error) { return "/bin/docker", nil }
	mounts := []dockerMount{
		{Source: "/data/html", Destination: "/usr/share/nginx/html"},
		{Source: "/data/nginx", Destination: "/etc/nginx"},
	}
	raw, err := json.Marshal(mounts)
	if err != nil {
		t.Fatal(err)
	}
	edgeInspectMountsJSON = func(string) ([]byte, error) { return raw, nil }

	layout, ok, err := layoutFromDockerMounts("edge-nginx")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if layout.ConfDDir != filepath.Join("/data/nginx", "conf.d") {
		t.Fatalf("ConfDDir=%q", layout.ConfDDir)
	}
	if layout.IncludesDir != filepath.Join("/data/nginx", "includes") {
		t.Fatalf("IncludesDir=%q", layout.IncludesDir)
	}
}

func TestConfHasMaintenanceInclude(t *testing.T) {
	if !confHasMaintenanceInclude("include /etc/nginx/includes/qabas1-maintenance.conf;") {
		t.Fatal("specific include")
	}
	if !confHasMaintenanceInclude("include /etc/nginx/includes/*.conf;") {
		t.Fatal("glob include")
	}
	if confHasMaintenanceInclude("server_name app.qabas.one;") {
		t.Fatal("should not match")
	}
}

func TestVerifyMaintenanceInclude(t *testing.T) {
	root := t.TempDir()
	confD := filepath.Join(root, "conf.d")
	if err := os.MkdirAll(confD, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := layoutFromRoot(root, "edge-nginx")

	if err := verifyMaintenanceInclude(layout); err == nil {
		t.Fatal("expected missing include error")
	} else if exitcode.HintOf(err) == "" {
		t.Fatalf("expected hint: %v", err)
	}

	conf := filepath.Join(confD, "30-qabas.one.conf")
	body := "server {\n    server_name app.qabas.one;\n    include /etc/nginx/includes/qabas1-maintenance.conf;\n}\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyMaintenanceInclude(layout); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyMaintenanceIncludeEnvConf(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "custom.conf")
	if err := os.WriteFile(conf, []byte("include /etc/nginx/includes/*.conf;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABAS_EDGE_NGINX_CONF", conf)
	layout := layoutFromRoot(filepath.Join(root, "unused"), "edge-nginx")
	if err := verifyMaintenanceInclude(layout); err != nil {
		t.Fatal(err)
	}
}

func TestEnableDisableDoesNotPatchConf(t *testing.T) {
	root := t.TempDir()
	confD := filepath.Join(root, "conf.d")
	if err := os.MkdirAll(confD, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(confD, "30-qabas.one.conf")
	original := "server {\n    server_name app.qabas.one;\n    include /etc/nginx/includes/qabas1-maintenance.conf;\n    location = /healthz { return 200; }\n}\n"
	if err := os.WriteFile(conf, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABAS_EDGE_NGINX_DIR", root)
	t.Setenv("QABAS_EDGE_NGINX_CONTAINER", "edge-nginx")

	origLook := edgeDockerLookPath
	origReload := edgeReloadNginx
	t.Cleanup(func() {
		edgeDockerLookPath = origLook
		edgeReloadNginx = origReload
	})
	edgeDockerLookPath = func(string) (string, error) { return "/bin/docker", nil }
	reloads := 0
	edgeReloadNginx = func(container string) error {
		reloads++
		if container != "edge-nginx" {
			t.Fatalf("container=%q", container)
		}
		return nil
	}

	until := time.Now().Add(30 * time.Minute).UTC()
	if err := enableEdgeMaintenance(until, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("conf was modified:\n%s", after)
	}
	flag := filepath.Join(root, "html", "qabas1-maintenance.enabled")
	if _, err := os.Stat(flag); err != nil {
		t.Fatal(err)
	}
	include := filepath.Join(root, "includes", "qabas1-maintenance.conf")
	if _, err := os.Stat(include); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, "html", "qabas1-maintenance.html")
	if _, err := os.Stat(page); err != nil {
		t.Fatal(err)
	}

	if err := disableEdgeMaintenance(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatalf("flag should be removed: %v", err)
	}
	afterOff, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterOff) != original {
		t.Fatal("conf modified on disable")
	}
	if reloads < 2 {
		t.Fatalf("expected reload on enable and disable, got %d", reloads)
	}
}

func TestEnableFailsWhenIncludeMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABAS_EDGE_NGINX_DIR", root)
	t.Setenv("QABAS_EDGE_NGINX_CONF", "")
	origLook := edgeDockerLookPath
	t.Cleanup(func() { edgeDockerLookPath = origLook })
	edgeDockerLookPath = func(string) (string, error) { return "", errors.New("skip") }

	err := enableEdgeMaintenance(time.Now().Add(time.Hour), "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "do not include the maintenance snippet") {
		t.Fatalf("got %v", err)
	}
	flag := filepath.Join(root, "html", "qabas1-maintenance.enabled")
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatal("should not write flag when include missing")
	}
}

func TestResolveMaintenancePagePath(t *testing.T) {
	t.Setenv("QABAS_MAINTENANCE_PAGE", "/from/env.html")
	if got := resolveMaintenancePagePath(""); got != "/from/env.html" {
		t.Fatalf("env: got %q", got)
	}
	if got := resolveMaintenancePagePath("/from/flag.html"); got != "/from/flag.html" {
		t.Fatalf("flag should win: got %q", got)
	}
	t.Setenv("QABAS_MAINTENANCE_PAGE", "")
	if got := resolveMaintenancePagePath(""); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestMaintenanceHTMLCustomPage(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "custom.html")
	if err := os.WriteFile(page, []byte("<h1>Down</h1><p>{{.Until}}</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	html, err := maintenanceHTML(until, page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "<h1>Down</h1>") {
		t.Fatalf("missing custom content: %s", html)
	}
	if strings.Contains(html, "{{.Until}}") {
		t.Fatal("placeholder not rendered")
	}
	if !strings.Contains(html, until.Local().Format("Monday, 02 Jan 2006 15:04 MST")) {
		t.Fatalf("missing until label: %s", html)
	}
}

func TestMaintenanceHTMLDefault(t *testing.T) {
	until := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	html, err := maintenanceHTML(until, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<html lang="ar" dir="rtl">`) {
		t.Fatalf("default page is not Arabic RTL: %s", html)
	}
	if !strings.Contains(html, "نعمل على تحسين الخدمة وسنعود قريبًا") {
		t.Fatalf("default page missing: %s", html)
	}
}

func TestEnableUsesCustomPage(t *testing.T) {
	root := t.TempDir()
	confD := filepath.Join(root, "conf.d")
	if err := os.MkdirAll(confD, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(confD, "app.conf")
	if err := os.WriteFile(conf, []byte("include /etc/nginx/includes/qabas1-maintenance.conf;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, "page.html")
	if err := os.WriteFile(page, []byte("<title>Custom</title>{{.Until}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABAS_EDGE_NGINX_DIR", root)
	t.Setenv("QABAS_MAINTENANCE_PAGE", page)

	origLook := edgeDockerLookPath
	origReload := edgeReloadNginx
	t.Cleanup(func() {
		edgeDockerLookPath = origLook
		edgeReloadNginx = origReload
	})
	edgeDockerLookPath = func(string) (string, error) { return "", errors.New("skip") }
	edgeReloadNginx = func(string) error { return nil }

	if err := enableEdgeMaintenance(time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(root, "html", "qabas1-maintenance.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<title>Custom</title>") {
		t.Fatalf("got %s", out)
	}
	inc, err := os.ReadFile(filepath.Join(root, "includes", "qabas1-maintenance.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inc), "Custom") {
		t.Fatalf("include missing custom html: %s", inc)
	}
}

func TestDeployAutomaticallyEnablesAndDisablesMaintenance(t *testing.T) {
	e, eng, plan, flag := automaticMaintenanceDeploy(t)

	err := runDeployWithMaintenance(e, eng, deploy.Options{Action: deploy.ActionDeploy}, plan, func() error {
		if err := eng.BeforeSwitch(); err != nil {
			return err
		}
		if _, err := os.Stat(flag); err != nil {
			t.Fatalf("maintenance flag not present during deploy: %v", err)
		}
		st, err := state.Load(e.Paths)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Maintenance {
			t.Fatal("state should be in maintenance during deploy")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatalf("maintenance flag should be removed after successful deploy: %v", err)
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.Maintenance || !st.MaintenanceUntil.IsZero() {
		t.Fatalf("maintenance should be off after successful deploy: %+v", st)
	}
}

func TestDeployFailureLeavesAutomaticMaintenanceOn(t *testing.T) {
	e, eng, plan, flag := automaticMaintenanceDeploy(t)
	deployErr := errors.New("switch failed")

	err := runDeployWithMaintenance(e, eng, deploy.Options{Action: deploy.ActionDeploy}, plan, func() error {
		if err := eng.BeforeSwitch(); err != nil {
			return err
		}
		return deployErr
	})
	if !errors.Is(err, deployErr) {
		t.Fatalf("got %v, want %v", err, deployErr)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatalf("maintenance flag should remain after failed deploy: %v", err)
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Maintenance {
		t.Fatal("maintenance should remain on after failed deploy")
	}
}

func TestDeployPreSwitchFailureDoesNotEnableMaintenance(t *testing.T) {
	e, eng, plan, flag := automaticMaintenanceDeploy(t)
	pullErr := errors.New("pull failed")

	err := runDeployWithMaintenance(e, eng, deploy.Options{Action: deploy.ActionDeploy}, plan, func() error {
		return pullErr
	})
	if !errors.Is(err, pullErr) {
		t.Fatalf("got %v, want %v", err, pullErr)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatalf("maintenance should not start before switch: %v", err)
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.Maintenance {
		t.Fatal("pre-switch failure should leave maintenance off")
	}
}

func TestDeployPreservesManualMaintenance(t *testing.T) {
	e, eng, plan, _ := automaticMaintenanceDeploy(t)
	eng.State.Maintenance = true
	eng.State.MaintenanceUntil = time.Now().Add(time.Hour)
	if err := state.Save(e.Paths, eng.State); err != nil {
		t.Fatal(err)
	}

	called := false
	if err := runDeployWithMaintenance(e, eng, deploy.Options{Action: deploy.ActionDeploy}, plan, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("deploy was not run")
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Maintenance {
		t.Fatal("manually enabled maintenance should be preserved")
	}
}

func automaticMaintenanceDeploy(t *testing.T) (*env, *deploy.Engine, *deploy.Plan, string) {
	t.Helper()
	root := t.TempDir()
	confD := filepath.Join(root, "edge", "conf.d")
	if err := os.MkdirAll(confD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(confD, "app.conf"),
		[]byte("include /etc/nginx/includes/qabas1-maintenance.conf;\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABAS_EDGE_NGINX_DIR", filepath.Join(root, "edge"))

	origLook := edgeDockerLookPath
	t.Cleanup(func() { edgeDockerLookPath = origLook })
	edgeDockerLookPath = func(string) (string, error) { return "", errors.New("skip reload") }

	p := paths.Paths{
		ConfigDir: filepath.Join(root, "config"),
		StateDir:  filepath.Join(root, "state"),
		LogDir:    filepath.Join(root, "log"),
		LockPath:  filepath.Join(root, "state", "qabas.lock"),
	}
	st := state.State{CurrentRelease: "2.8.0"}
	if err := state.Save(p, st); err != nil {
		t.Fatal(err)
	}
	e := &env{Paths: p, Out: output.New(false, true)}
	eng := &deploy.Engine{State: st}
	plan := &deploy.Plan{Steps: []string{"switch"}}
	flag := filepath.Join(root, "edge", "html", "qabas1-maintenance.enabled")
	return e, eng, plan, flag
}
