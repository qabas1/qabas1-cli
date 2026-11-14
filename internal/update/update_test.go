package update_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/update"
)

func TestNeedsUpdate(t *testing.T) {
	if !update.NeedsUpdate("1.3.0", "1.4.1") {
		t.Fatal("expected update")
	}
	if update.NeedsUpdate("1.4.1", "1.4.1") {
		t.Fatal("same")
	}
	if update.NeedsUpdate("1.5.0", "1.4.1") {
		t.Fatal("newer current")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "qabas_1.2.3_linux_amd64.deb")
	if err := os.WriteFile(asset, []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("bc4a71180870f7945155fbb02f4b0a2e3faa2a62d6d31b7039013055ed19869a  qabas_1.2.3_linux_amd64.deb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := update.VerifyChecksum(sums, asset, "qabas_1.2.3_linux_amd64.deb"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "qabas")
	if err := os.WriteFile(asset, []byte("new-binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("0000000000000000000000000000000000000000000000000000000000000000  qabas\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := update.VerifyChecksum(sums, asset, "qabas"); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestCheckTrackDevUsesDevLatestAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/qabasone/qabasone-cli/releases/tags/dev-latest") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		resp := map[string]any{
			"tag_name": "dev-latest",
			"assets": []map[string]any{
				{"id": 1, "name": "SHA256SUMS", "browser_download_url": serverURL(r) + "/SHA256SUMS"},
				{"id": 2, "name": "qabas_dev_" + runtimePair() + ".deb", "browser_download_url": serverURL(r) + "/qabas.deb"},
				{"id": 3, "name": "qabas_dev_" + runtimePair() + ".msi", "browser_download_url": serverURL(r) + "/qabas.msi"},
				{"id": 4, "name": "qabas_dev_" + runtimePair() + ".exe", "browser_download_url": serverURL(r) + "/qabas.exe"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	info, err := update.CheckTrack(server.URL, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if info.Latest != "dev" {
		t.Fatalf("expected latest dev, got %q", info.Latest)
	}
	// Linux CI has dpkg (.deb); a test binary on Windows is not under Program Files (.exe).
	wantSuffix := ".deb"
	if runtime.GOOS == "windows" {
		wantSuffix = ".exe"
	}
	if !strings.HasPrefix(info.Asset, "qabas_dev_") || !strings.HasSuffix(info.Asset, wantSuffix) {
		t.Fatalf("expected dev %s asset, got %q", wantSuffix, info.Asset)
	}
}

func TestCheckStablePrefersDebPackage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux package selection")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/qabasone/qabasone-cli/releases/latest") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		resp := map[string]any{
			"tag_name": "v1.2.3",
			"assets": []map[string]any{
				{"id": 1, "name": "qabas_1.2.3_" + runtimePair(), "browser_download_url": serverURL(r) + "/qabas"},
				{"id": 2, "name": "qabas_1.2.3_" + runtimePair() + ".deb", "browser_download_url": serverURL(r) + "/qabas.deb"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	info, err := update.CheckTrack(server.URL, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if info.Asset != "qabas_1.2.3_"+runtimePair()+".deb" {
		t.Fatalf("expected deb asset, got %q", info.Asset)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func runtimePair() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}
