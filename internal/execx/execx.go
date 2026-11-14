// Package execx builds os/exec commands that are shielded from terminal
// SIGINT (Unix process-group detachment) so critical work is not torn down
// when the parent defers Ctrl+C.
package execx

import "os/exec"

// Command is like exec.Command but detaches the child from the controlling
// terminal's process group on Unix so Ctrl+C does not deliver SIGINT to it.
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	detach(cmd)
	return cmd
}
