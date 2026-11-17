// Package execx builds os/exec commands that are shielded from terminal
// SIGINT (Unix process-group detachment) so critical work is not torn down
// when the parent defers Ctrl+C.
package execx

import "os/exec"

// Command is like exec.Command but shields the child from terminal Ctrl+C
// during interrupt.Protect critical sections. Other commands share the
// terminal's process group so interactive commands stop on Ctrl+C.
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	detach(cmd)
	return cmd
}
