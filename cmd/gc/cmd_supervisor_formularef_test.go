// Scope: the supervisor control socket's "formularef" command, the client
// that reads its answer, and the formula-source fields `gc supervisor
// status` reports from it.
//
// Why this suite exists: a supervisor's effective GC_FORMULA_REF cannot be
// read from outside the process at all. os.Setenv does NOT rewrite
// /proc/<pid>/environ -- Go keeps its own copy and the kernel block is a
// snapshot taken at exec -- so a supervisor pinned by
// applySupervisorFormulaRef reads as UNPINNED to the one check an operator
// makes against a live process. supervisorChildEnv makes that check
// truthful for `gc supervisor start`, which has a fork to seed, and cannot
// help `gc supervisor run` under systemd, launchd or a bare foreground
// launch, which has none. Measured on this host: the environ read reported
// "not pinned" on a pinned city, twice in one session (ci-fn48qz).
//
// Both wrong answers cost. Told "pinned" wrongly, an operator commits a
// formula edit that was already live; told "working tree" wrongly, an agent
// edits a formula, sees nothing happen, and cannot tell a broken formula
// from an uncommitted one.
//
// Every case drives net.Pipe rather than a unix listener, mirroring
// cmd_supervisor_packhash_test.go: test/test-resources.toml ratchets the
// untagged net.Listen count and forbids growth. What this suite therefore
// CANNOT represent is the dial -- a wrong socket path, a refused connection
// -- which reaches the caller as ok=false the same way silence does.
//
// It delegates which raw values mean "working tree" to
// internal/formula/source_test.go, where the answer is derived from
// SourceFromEnv itself rather than restated.
//
// Run: go test ./cmd/gc/ -run SupervisorFormulaRef

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSupervisorFormulaRefAnswersThePinTheProcessHolds is the round trip.
// The value is read from the supervisor's own live environment, so the
// answer must move when that environment does -- a constant would satisfy
// any single-value assertion.
func TestSupervisorFormulaRefAnswersThePinTheProcessHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		env  string
		want string
	}{
		{"pinned to a branch", true, "main", "ref:main"},
		{"unset", false, "", "working-tree"},
		// Set-but-empty is NOT the same fact as unset, and both resolve to
		// the working tree. The distinction is provenance only, so the
		// answer collapses them deliberately: a reader acting on this is
		// asking whether an uncommitted formula edit is live, and for that
		// question the two are one state.
		{"set empty", true, "", "working-tree"},
		// SourceFromEnv treats these as the working tree, so reporting them
		// as a pin would be a confident wrong answer in the expensive
		// direction. Keying the report on "is the variable present" instead
		// of on the resolver's own rule is exactly that mistake.
		{"explicit working-tree", true, "working-tree", "working-tree"},
		{"HEAD", true, "HEAD", "working-tree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(supervisorFormulaRefEnv, tc.env)
			} else {
				unsetFormulaRefForTest(t)
			}
			if got := supervisorSocketAsk(t, "formularef"); got != tc.want {
				t.Fatalf("formularef answered %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSupervisorFormulaRefClientReadsTheAnswer covers the client half.
func TestSupervisorFormulaRefClientReadsTheAnswer(t *testing.T) {
	t.Setenv(supervisorFormulaRefEnv, "release/1.4")
	ref, pinned, ok := formulaRefOverScriptedPeer(t, func(conn net.Conn) {
		handleSupervisorConn(conn, nil, nil, nil)
	})
	if !ok {
		t.Fatal("client reported no answer from a supervisor that answers")
	}
	if !pinned || ref != "release/1.4" {
		t.Fatalf("client read (ref=%q pinned=%v), want (release/1.4 true)", ref, pinned)
	}
}

// TestSupervisorFormulaRefClientRejectsASilentSupervisor pins the case that
// exists in production today: every supervisor now running predates this
// command, accepts the connection, recognizes nothing and closes. That
// silence must report ok=false and NEVER "working tree", which is a real
// answer an operator would act on.
func TestSupervisorFormulaRefClientRejectsASilentSupervisor(t *testing.T) {
	assertFormulaRefAnswerRejected(t, "")
}

// TestSupervisorFormulaRefClientRejectsAnUnknownAnswer keeps a wedged or
// mis-wired socket from being read as a verdict. "busy" and "timeout" are
// real lines this socket emits for other commands, so an answer-shaped
// check that accepted any line would report one of them as a formula ref.
func TestSupervisorFormulaRefClientRejectsAnUnknownAnswer(t *testing.T) {
	for _, answer := range []string{"busy", "timeout", "ok", "12345", "ref:"} {
		t.Run(answer, func(t *testing.T) {
			assertFormulaRefAnswerRejected(t, answer)
		})
	}
}

// TestSupervisorFormulaRefStatusJSONCarriesTheSource pins the operator-facing
// half: the fields are what replaces the /proc read in the city's own
// recipe, so their absence is the whole defect surviving the fix.
func TestSupervisorFormulaRefStatusJSONCarriesTheSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		answer     string
		wantSource string
		wantRef    any
	}{
		{"pinned", "ref:main", "ref", "main"},
		{"working tree", "working-tree", "working-tree", nil},
		// An unanswering supervisor must leave the fields OUT rather than
		// default them. A defaulted "working-tree" here is the false
		// negative this bead was filed for, restated in JSON.
		{"no answer", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{}
			addSupervisorFormulaSource(payload, tc.answer, tc.answer != "")
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if tc.wantSource == "" {
				if _, present := got["formula_source"]; present {
					t.Fatalf("payload = %v, want no formula_source from a silent supervisor", got)
				}
				return
			}
			if got["formula_source"] != tc.wantSource {
				t.Fatalf("formula_source = %v, want %q", got["formula_source"], tc.wantSource)
			}
			if tc.wantRef == nil {
				if _, present := got["formula_ref"]; present {
					t.Fatalf("payload = %v, want no formula_ref when unpinned", got)
				}
				return
			}
			if got["formula_ref"] != tc.wantRef {
				t.Fatalf("formula_ref = %v, want %v", got["formula_ref"], tc.wantRef)
			}
		})
	}
}

// supervisorSocketAsk sends one command to a real handleSupervisorConn over
// net.Pipe and returns the single line it answers with.
func supervisorSocketAsk(t *testing.T, command string) string {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() }) //nolint:errcheck
	go handleSupervisorConn(server, nil, nil, nil)
	if _, err := client.Write([]byte(command + "\n")); err != nil {
		t.Fatalf("Write(%s): %v", command, err)
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	return strings.TrimSpace(line)
}

// assertFormulaRefAnswerRejected drives the client against a peer that
// answers exactly once with answer, or closes without answering when answer
// is empty. The empty case IS the silent-supervisor case, so the helper
// must not turn it into a bare newline. The peer scripts nothing else: a
// stand-in that replied successfully to anything would hand a pass to
// whatever this suite forgot to script.
func assertFormulaRefAnswerRejected(t *testing.T, answer string) {
	t.Helper()
	ref, pinned, ok := formulaRefOverScriptedPeer(t, func(conn net.Conn) {
		bufio.NewScanner(conn).Scan()
		if answer != "" {
			conn.Write([]byte(answer + "\n")) //nolint:errcheck
		}
		conn.Close() //nolint:errcheck
	})
	if ok {
		t.Fatalf("client accepted %q as a formula source (ref=%q pinned=%v)", answer, ref, pinned)
	}
}

func formulaRefOverScriptedPeer(t *testing.T, peer func(net.Conn)) (string, bool, bool) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() }) //nolint:errcheck
	go peer(server)
	return supervisorFormulaSourceOverConn(client, 5*time.Second)
}

// unsetFormulaRefForTest makes the key ABSENT for the duration of the test,
// which t.Setenv alone cannot express. Setting it first is what registers
// the restore and the no-parallel marker; the Unsetenv is the state under
// test. Absent and set-empty are different facts and both have to be
// reachable, or the case that distinguishes them cannot be written.
func unsetFormulaRefForTest(t *testing.T) {
	t.Helper()
	t.Setenv(supervisorFormulaRefEnv, "")
	os.Unsetenv(supervisorFormulaRefEnv) //nolint:errcheck
}
