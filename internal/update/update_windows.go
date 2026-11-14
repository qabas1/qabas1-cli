//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	detachedProcess      = 0x00000008
	msiProductName       = "qabas"
	msiProductPublisher  = "QabasOne"
	uninstallRegistryKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
)

// installedViaMSI reports whether the running qabas.exe lives under Program
// Files (the MSI install location) rather than the install.ps1 per-user dir.
func installedViaMSI() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(exe), strings.ToLower(filepath.Join(root, "qabas"))+`\`) {
			return true
		}
	}
	return false
}

// applyMSI starts a detached, silent msiexec upgrade and returns immediately
// so the running qabas.exe can exit before Windows Installer replaces it.
func applyMSI(msiPath string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return exitcode.NewHint(exitcode.InvalidInput,
			"re-run `qabas update` from an elevated PowerShell (Run as administrator)",
			"installing the MSI package requires administrator rights")
	}
	logPath := strings.TrimSuffix(msiPath, filepath.Ext(msiPath)) + ".log"
	cmd := exec.Command("msiexec.exe", "/i", msiPath, "/qn", "/norestart", "/l*v", logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return exitcode.New(exitcode.Generic, "start msiexec: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// UninstallInstructions returns the exact command that removes the Windows CLI.
func UninstallInstructions() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if installedViaMSI() {
		if code := msiProductCode(); code != "" {
			return fmt.Sprintf("msiexec /x %s", code), nil
		}
		return "Settings > Apps > Installed apps > qabas > Uninstall", nil
	}
	dir := filepath.Dir(exe)
	if isScriptInstallDir(dir) {
		return fmt.Sprintf("Remove-Item -Recurse -Force '%s'  (then remove %s from your user PATH)", dir, dir), nil
	}
	return fmt.Sprintf("Remove-Item -Force '%s'", exe), nil
}

// isScriptInstallDir reports whether dir is the dedicated install.ps1 folder
// (%LOCALAPPDATA%\Programs\qabas), which is safe to delete as a whole.
func isScriptInstallDir(dir string) bool {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(dir), filepath.Join(local, "Programs", "qabas"))
}

func msiProductCode() string {
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		root, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRegistryKey, registry.ENUMERATE_SUB_KEYS|registry.READ|view)
		if err != nil {
			continue
		}
		names, _ := root.ReadSubKeyNames(-1)
		root.Close()
		for _, name := range names {
			k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRegistryKey+`\`+name, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			display, _, _ := k.GetStringValue("DisplayName")
			publisher, _, _ := k.GetStringValue("Publisher")
			k.Close()
			if display == msiProductName && publisher == msiProductPublisher && strings.HasPrefix(name, "{") {
				return name
			}
		}
	}
	return ""
}
