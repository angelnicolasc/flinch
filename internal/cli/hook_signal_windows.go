//go:build windows

package cli

// ignoreBrokenPipe has nothing to do on Windows, where a write to a closed
// pipe returns an error instead of raising a signal.
func ignoreBrokenPipe() {}
