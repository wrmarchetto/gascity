package contract

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is the procfs root the managed-port ownership check reads. Tests
// point it at a constructed tree; nothing in production overrides it.
//
// There is deliberately NO refuse-the-live-/proc guard here, unlike
// proctable.liveScanGuard. That guard exists because enumerating the real
// /proc feeds an orphan sweep that SIGTERMs live agents. This reader only
// reads two text files and one fd directory, kills nothing, and the
// connection tests in this package depend on it answering for listeners they
// bind in the test process itself.
var procRoot = "/proc"

// procListenState is the /proc/net/tcp st column for a socket in LISTEN.
// Values are the kernel's TCP_* enum, not the textual state names ss prints.
const procListenState = "0A"

// pidHoldsListeningPort reports whether pid holds a listening TCP socket on
// port, and whether that question could be answered at all. Returns
// (holds, answered).
//
// The caller wants "is the process this state file names serving this port",
// and a TCP dial cannot answer that: it reports only that SOMETHING answers,
// so a recycled PID whose number now belongs to an unrelated process reads as
// healthy for as long as anything else holds the port. Reading the socket
// table answers the real question and costs the Dolt server no accept, which
// is the other half of why this exists -- the dial it replaces was a full
// accept on a port the caller was about to connect to anyway.
//
// answered=false means the question could not be decided -- no procfs (every
// non-Linux host, and restricted containers), or a listener exists but its
// owner cannot be attributed. It must NEVER be conflated with holds=false:
// the caller falls back to the dial on cannot-answer, and reporting "not
// listening" there would condemn a healthy server to the recovery path.
//
// The socket table is the READER's network namespace. A server in a different
// netns is invisible here, but it is equally unreachable by the dial from
// this namespace, so the two agree; the container deployments that genuinely
// reach across a namespace do it through a non-local GC_DOLT_HOST, which
// never reaches this check.
func pidHoldsListeningPort(pid, port int) (bool, bool) {
	if pid <= 0 || port <= 0 {
		return false, false
	}
	inodes, readable := listeningSocketInodes(port)
	if !readable {
		return false, false
	}
	if len(inodes) == 0 {
		// Nothing anywhere in this namespace is listening on the port. That
		// is a definitive answer and needs no fd table: a dial would fail too.
		return false, true
	}
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		// A failed readlink is an fd closed between the ReadDir and here, not
		// a permission problem -- ReadDir would have failed for that.
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
		if _, ok := inodes[inode]; ok {
			return true, true
		}
	}
	return false, true
}

// listeningSocketInodes returns the socket inodes listening on port across
// both address families, and whether any socket table could be read. A v6
// table alone is enough: a server bound to [::1] appears only there, and
// reading /proc/net/tcp alone would report it absent.
func listeningSocketInodes(port int) (map[string]struct{}, bool) {
	inodes := map[string]struct{}{}
	readable := false
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(procRoot, "net", name))
		if err != nil {
			continue
		}
		readable = true
		for _, line := range strings.Split(string(data), "\n") {
			// Columns: sl local_address rem_address st ... inode. The header
			// line and any short line fall out on the length check.
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != procListenState {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			got, err := strconv.ParseUint(portHex, 16, 16)
			if err != nil || int(got) != port {
				continue
			}
			inodes[fields[9]] = struct{}{}
		}
	}
	return inodes, readable
}
