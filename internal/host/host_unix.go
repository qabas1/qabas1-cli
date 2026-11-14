//go:build unix

package host

import "os"

func isPrivileged() bool { return os.Geteuid() == 0 }
