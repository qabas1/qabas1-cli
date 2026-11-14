//go:build !windows

package update

import "github.com/qabasone/qabasone-cli/internal/exitcode"

func installedViaMSI() bool { return false }

func applyMSI(string) error {
	return exitcode.New(exitcode.InvalidInput, "MSI packages can only be installed on Windows")
}

// UninstallInstructions is only meaningful on Windows; Linux uses apt.
func UninstallInstructions() (string, error) { return "", nil }
