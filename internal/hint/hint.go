// Package hint renders operator-facing commands for the current OS: Linux
// hosts run qabas through sudo, Windows hosts from an elevated PowerShell.
package hint

import (
	"runtime"
	"strings"
)

// ElevatedNote is appended to standalone Windows hints that need admin rights.
const ElevatedNote = " (from an elevated PowerShell)"

// Cmd renders a qabas command, e.g. Cmd("qabas install") is "sudo qabas install"
// on Unix and "qabas install" on Windows.
func Cmd(cmd string) string { return cmdFor(runtime.GOOS, cmd, false) }

// CmdEnv is Cmd for commands that read credentials from the caller's
// environment ("sudo -E qabas ..." on Unix).
func CmdEnv(cmd string) string { return cmdFor(runtime.GOOS, cmd, true) }

// Run renders "run `<cmd>`" with the elevation note on Windows.
func Run(cmd string) string { return runFor(runtime.GOOS, cmd) }

// Localize rewrites Unix-style command text (sudo, export) for the current OS.
// It is a no-op outside Windows.
func Localize(s string) string { return localizeFor(runtime.GOOS, s) }

func cmdFor(goos, cmd string, keepEnv bool) string {
	cmd = strings.TrimSpace(cmd)
	if goos == "windows" {
		return cmd
	}
	if keepEnv {
		return "sudo -E " + cmd
	}
	return "sudo " + cmd
}

func runFor(goos, cmd string) string {
	s := "run `" + cmdFor(goos, cmd, false) + "`"
	if goos == "windows" {
		s += ElevatedNote
	}
	return s
}

var windowsReplacer = strings.NewReplacer(
	`export GITHUB_TOKEN="$(gh auth token)" && `, `$env:GITHUB_TOKEN = (gh auth token); `,
	`export GITHUB_TOKEN="$(gh auth token)"`, `$env:GITHUB_TOKEN = (gh auth token)`,
	`export GHCR_TOKEN="…" && `, `$env:GHCR_TOKEN = "…"; `,
	"sudo -E qabas", "qabas",
	"sudo -E ", "",
	"sudo qabas", "qabas",
	"sudo rm ", "Remove-Item ",
)

func localizeFor(goos, s string) string {
	if goos != "windows" || s == "" {
		return s
	}
	return windowsReplacer.Replace(s)
}
