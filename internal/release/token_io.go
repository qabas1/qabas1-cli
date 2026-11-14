package release

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

func readTokenFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sudoUserConfigFile(basename string) string {
	su := os.Getenv("SUDO_USER")
	if su == "" || su == "root" {
		return ""
	}
	u, err := user.Lookup(su)
	if err != nil || u.HomeDir == "" {
		return ""
	}
	return filepath.Join(u.HomeDir, ".config", "qabas", basename)
}

func ghAuthToken() string {
	return runGH("auth", "token")
}

func ghAPIUserLogin() string {
	return runGH("api", "user", "-q", ".login")
}

func runGH(args ...string) string {
	su := os.Getenv("SUDO_USER")
	var cmd *exec.Cmd
	if su != "" && su != "root" && os.Geteuid() == 0 {
		cmdArgs := append([]string{"-u", su, "-H", "gh"}, args...)
		cmd = exec.Command("sudo", cmdArgs...)
	} else {
		cmd = exec.Command("gh", args...)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
