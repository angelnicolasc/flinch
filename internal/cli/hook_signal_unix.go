//go:build !windows

package cli

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe turns a write to a host pipe that already closed into an
// ordinary error. Otherwise Go ends the process with SIGPIPE when standard
// output is a broken pipe, and the host would see a signal instead of exit 0.
func ignoreBrokenPipe() {
	signal.Ignore(syscall.SIGPIPE)
}
