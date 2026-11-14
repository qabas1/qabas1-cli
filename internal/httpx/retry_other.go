//go:build !windows

package httpx

func platformRetryable(error) bool { return false }
