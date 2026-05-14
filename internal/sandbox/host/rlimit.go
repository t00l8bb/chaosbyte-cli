package host

import (
	"fmt"
	"strings"
)

// rlimitCaps holds the per-process resource caps applied to every
// sandboxed command. Values are tuned for interactive dev work:
// generous enough that real builds and tests run, tight enough that
// a runaway loop or fork bomb does not take down the host.
//
// All values are POSIX `ulimit` units. CPU seconds, memory KB, open
// files count, file size KB.
type rlimitCaps struct {
	cpuSeconds int // -t
	memKB      int // -v
	openFiles  int // -n
	fileSizeKB int // -f
}

// defaultCaps is the system-wide default. Five minutes of CPU time,
// 2 GB address space, 256 open files, 1 GB max file size.
var defaultCaps = rlimitCaps{
	cpuSeconds: 300,
	memKB:      2 * 1024 * 1024,
	openFiles:  256,
	fileSizeKB: 1 * 1024 * 1024,
}

// wrapWithRlimit returns an argv that runs the supplied command under
// a POSIX shell `ulimit` wrapper. The shell sets each limit and then
// execs the user's command, replacing itself so no extra process
// remains in the tree.
//
// The wrapper layout:
//
//	/bin/sh -c 'ulimit -t T -v V -n N -f F; exec "$@"' rlimit-stub <argv...>
//
// "rlimit-stub" becomes $0 inside the shell; $1.. are the user's
// argv elements. exec "$@" replaces the shell with the user's
// process, so signal delivery and exit code propagate as expected.
func wrapWithRlimit(caps rlimitCaps, argv []string) []string {
	var ulimits []string
	if caps.cpuSeconds > 0 {
		ulimits = append(ulimits, fmt.Sprintf("ulimit -t %d", caps.cpuSeconds))
	}
	if caps.memKB > 0 {
		ulimits = append(ulimits, fmt.Sprintf("ulimit -v %d", caps.memKB))
	}
	if caps.openFiles > 0 {
		ulimits = append(ulimits, fmt.Sprintf("ulimit -n %d", caps.openFiles))
	}
	if caps.fileSizeKB > 0 {
		ulimits = append(ulimits, fmt.Sprintf("ulimit -f %d", caps.fileSizeKB))
	}
	if len(ulimits) == 0 {
		return argv
	}
	script := strings.Join(ulimits, "; ") + `; exec "$@"`
	out := []string{"/bin/sh", "-c", script, "rlimit-stub"}
	out = append(out, argv...)
	return out
}
