// Scope: the supervisor control socket's "packhash" command and the client
// that reads its answer.
//
// The suite exists because the value it carries cannot be read any other
// way: bundled packs are embedded in the gc binary, the supervisor outlives
// a `go install`, and os.Executable() then names a file whose contents have
// moved on. Only the running image can report what it is executing, so the
// round trip itself is the contract.
//
// Every case drives net.Pipe rather than a unix listener. That is not
// convenience: test/test-resources.toml ratchets the untagged net.Listen
// count and forbids growth, so the dial is split out into
// supervisorBundledPackHashAtPath and what this suite exercises is
// supervisorBundledPackHashOverConn. What it therefore CANNOT represent is
// the dial -- a wrong socket path, a refused connection -- which reaches
// the caller as ok=false the same way silence does.
//
// What it delegates: the verdict drawn from contrasting the two hashes is
// internal/doctor's (checks_supervisor_pack_drift_test.go), and the hash's
// own construction is internal/builtinpacks'.
//
// Run: go test ./cmd/gc/ -run SupervisorPackHash

package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/builtinpacks"
)

func TestSupervisorPackHashAnswersWithThisImagesPackContent(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck
	defer server.Close() //nolint:errcheck

	go handleSupervisorConn(server, nil, nil, nil)

	if _, err := client.Write([]byte("packhash\n")); err != nil {
		t.Fatalf("Write(packhash): %v", err)
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	got, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	got = strings.TrimSpace(got)

	// Comparing against SyntheticCacheKeyComponent alone would agree with
	// itself if the function ever returned "" -- both sides would be empty
	// and the socket could be answering nothing. Pin the shape too.
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("packhash answered %q, want a sha256: digest", got)
	}
	if want := builtinpacks.SyntheticCacheKeyComponent(); got != want {
		t.Fatalf("packhash = %q, want this binary's bundled pack hash %q", got, want)
	}
}

func TestSupervisorPackHashClientReadsTheAnswer(t *testing.T) {
	got, ok := packHashOverScriptedPeer(t, func(conn net.Conn) {
		handleSupervisorConn(conn, nil, nil, nil)
	})
	if !ok {
		t.Fatal("client reported no answer from a supervisor that answers")
	}
	if want := builtinpacks.SyntheticCacheKeyComponent(); got != want {
		t.Fatalf("client read %q, want %q", got, want)
	}
}

// TestSupervisorPackHashClientRejectsASilentSupervisor pins the case this
// check exists for today: the supervisor now running predates the packhash
// command, accepts the connection, recognizes no command and closes. That
// silence must NOT read as agreement, so the client reports ok=false rather
// than an empty hash a comparison would have to interpret.
func TestSupervisorPackHashClientRejectsASilentSupervisor(t *testing.T) {
	assertPackHashAnswerRejected(t, "")
}

// TestSupervisorPackHashClientRejectsATruncatedHash pins the read this
// client cannot retry: a short read of a genuine hash still begins
// "sha256:", so a prefix-only check would hand the drift comparison a
// truncated digest and report a mismatch the operator cannot reproduce.
func TestSupervisorPackHashClientRejectsATruncatedHash(t *testing.T) {
	assertPackHashAnswerRejected(t, builtinpacks.SyntheticCacheKeyComponent()[:20])
}

// TestSupervisorPackHashClientRejectsARightLengthNonHexAnswer covers the
// digits the length check cannot: a line of the exact hash length is
// otherwise accepted whole, and any diagnostic text the socket learns to
// emit at that length would silently become one side of the comparison.
func TestSupervisorPackHashClientRejectsARightLengthNonHexAnswer(t *testing.T) {
	assertPackHashAnswerRejected(t, "sha256:"+strings.Repeat("z", 64))
}

// TestSupervisorPackHashClientRejectsANonHashAnswer keeps a wedged or
// mis-wired socket from being read as a hash. Without it, any line at all
// would become one side of the drift comparison.
func TestSupervisorPackHashClientRejectsANonHashAnswer(t *testing.T) {
	assertPackHashAnswerRejected(t, "busy")
}

// assertPackHashAnswerRejected drives the client against a peer that
// answers exactly once with answer, or closes without answering when answer
// is empty. The empty case IS the silent-supervisor case, so the helper
// must not turn it into a bare newline.
func assertPackHashAnswerRejected(t *testing.T, answer string) {
	t.Helper()
	got, ok := packHashOverScriptedPeer(t, func(conn net.Conn) {
		bufio.NewScanner(conn).Scan()
		if answer != "" {
			conn.Write([]byte(answer + "\n")) //nolint:errcheck
		}
		conn.Close() //nolint:errcheck
	})
	if ok {
		t.Fatalf("client accepted %q as a bundled pack hash", got)
	}
}

// packHashOverScriptedPeer runs peer as the supervisor end of a net.Pipe and
// returns what the client made of it. The peer answers only what its case
// scripted: a stand-in that replied successfully to anything would hand a
// pass to whatever this suite forgot to script.
func packHashOverScriptedPeer(t *testing.T, peer func(net.Conn)) (string, bool) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() }) //nolint:errcheck
	go peer(server)
	return supervisorBundledPackHashOverConn(client, 5*time.Second)
}
