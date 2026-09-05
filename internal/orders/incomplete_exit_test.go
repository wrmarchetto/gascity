package orders

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestParseIncompleteExitCodes pins that an exec order can declare the exit
// statuses meaning "ran to completion, work remains" and that the declaration
// survives TOML decode as an ordered set of integers.
//
// It is a parse test rather than a struct-literal test because the whole point
// of the field is that an order AUTHOR writes it: a field the Go struct has and
// the TOML decoder drops would leave every declaring order silently classified
// as failing, which is the defect this exists to fix (ci-iv9asy).
func TestParseIncompleteExitCodes(t *testing.T) {
	a, err := Parse([]byte(`
[order]
exec = "scripts/sweep.sh"
trigger = "cooldown"
interval = "30m"
incomplete_exit_codes = [3, 4]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := a.IncompleteExitCodes; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("IncompleteExitCodes = %v, want [3 4]", got)
	}
}

// TestIsIncompleteExitMatchesOnlyDeclaredCodes pins the membership predicate
// the dispatcher classifies with. Exit 0 is excluded deliberately even when
// declared: a zero exit is already a success, and letting it match here would
// silently downgrade a clean run to "incomplete".
func TestIsIncompleteExitMatchesOnlyDeclaredCodes(t *testing.T) {
	a := Order{IncompleteExitCodes: []int{3}}
	for code, want := range map[int]bool{0: false, 1: false, 2: false, 3: true, 4: false, -1: false} {
		if got := a.IsIncompleteExit(code); got != want {
			t.Errorf("IsIncompleteExit(%d) = %v, want %v", code, got, want)
		}
	}
	var none Order
	if none.IsIncompleteExit(1) {
		t.Error("IsIncompleteExit(1) on an order declaring nothing = true, want false")
	}
}

// TestValidateIncompleteExitCodes pins the load-time refusals. Each one exists
// because the alternative is a declaration that reads as effective and is not:
// on a formula order the dispatcher never sees an exit status at all, code 0 is
// already success, and a code outside 1-255 is unreachable through a process
// exit status so it could only ever be a typo.
func TestValidateIncompleteExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		order   Order
		wantSub string
	}{
		{
			name:  "declared on an exec order",
			order: Order{Name: "sweep", Exec: "s.sh", Trigger: "manual", IncompleteExitCodes: []int{3}},
		},
		{
			name:    "declared on a formula order",
			order:   Order{Name: "sweep", Formula: "f", Trigger: "manual", IncompleteExitCodes: []int{3}},
			wantSub: "incomplete_exit_codes is supported only for exec orders",
		},
		{
			name:    "zero is not an incomplete code",
			order:   Order{Name: "sweep", Exec: "s.sh", Trigger: "manual", IncompleteExitCodes: []int{0}},
			wantSub: "invalid incomplete_exit_codes entry 0",
		},
		{
			name:    "above the process exit range",
			order:   Order{Name: "sweep", Exec: "s.sh", Trigger: "manual", IncompleteExitCodes: []int{256}},
			wantSub: "invalid incomplete_exit_codes entry 256",
		},
		{
			name:    "negative",
			order:   Order{Name: "sweep", Exec: "s.sh", Trigger: "manual", IncompleteExitCodes: []int{-1}},
			wantSub: "invalid incomplete_exit_codes entry -1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.order)
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate = %v, want error containing %q", err, tt.wantSub)
			}
		})
	}
}

// TestRunOutcomeExecIncompleteIsNotAFailure pins the outcome that carries the
// distinction end to end. "incomplete" must NOT display as "failed": every
// consumer that counts consecutive failures -- the order-firing doctor check
// above all -- reads Display(), so a shared label here would put the false
// alarm straight back (ci-iv9asy).
func TestRunOutcomeExecIncompleteIsNotAFailure(t *testing.T) {
	if got := RunOutcomeExecIncomplete.Display(); got != "incomplete" {
		t.Errorf("Display() = %q, want %q", got, "incomplete")
	}
	if !RunOutcomeExecIncomplete.IsExec() {
		t.Error("IsExec() = false, want true")
	}
	labels := RunOutcomeExecIncomplete.Labels()
	if len(labels) != 1 || labels[0] != "exec-incomplete" {
		t.Fatalf("Labels() = %v, want [exec-incomplete]", labels)
	}
	if got := outcomeFromLabels(labels); got != RunOutcomeExecIncomplete {
		t.Errorf("outcomeFromLabels(%v) = %v, want RunOutcomeExecIncomplete", labels, got)
	}
	// A closed incomplete run is a finished run, not a failed one: the order
	// feed ranks "failed" ahead of everything, and an ordinary unfinished
	// sweep must not sit at the top of it.
	if got := (OrderRun{Outcome: RunOutcomeExecIncomplete}).State(); got != "completed" {
		t.Errorf("State() = %q, want %q", got, "completed")
	}
}

// TestOrderDecodeCoversEveryTOMLField pins that orderDecode carries a field for
// every TOML key Order declares, and that normalized() copies it through.
//
// It exists because the shadow decode struct is invisible from the Order type:
// adding IncompleteExitCodes to Order alone compiled, passed vet, and silently
// dropped every declaration at parse time, which would have shipped an order
// field that reads as configured and does nothing. A reviewer cannot see the
// omission from either struct on its own, so the check has to be mechanical.
//
// It compares TOML key sets rather than Go field names: the two structs name
// the same key differently in places (TZ/tz), and the key is what an order
// author writes. Fields tagged `toml:"-"` are scanner-set, never parsed, and
// are excluded by the same rule that excludes them from the decode struct.
func TestOrderDecodeCoversEveryTOMLField(t *testing.T) {
	orderKeys := tomlKeysOf(t, reflect.TypeOf(Order{}))
	decodeKeys := tomlKeysOf(t, reflect.TypeOf(orderDecode{}))
	for _, key := range orderKeys {
		if !slices.Contains(decodeKeys, key) {
			t.Errorf("Order declares toml key %q with no orderDecode field: every declaration of it would parse to the zero value", key)
		}
	}

	// normalized() is the second half: a decode field nothing copies is the
	// same silent drop one layer down. Compare a fully-populated decode
	// against its normalized Order for any TOML-carried key left at zero.
	populated := orderDecode{
		Description: "d", Formula: "f", Exec: "e", Scope: "city", Trigger: "manual",
		Interval: "1m", Schedule: "* * * * *", TZ: "UTC", Check: "c", On: "o",
		Pool: "p", Timeout: "1m", CheckTimeout: "1m", Enabled: new(bool),
		Idempotent: true, NoWorkGate: true,
		Env: map[string]string{"K": "V"}, Params: map[string]OrderParam{"x": {}},
		SkipAliases: []string{"alias"}, IncompleteExitCodes: []int{3},
	}
	got := reflect.ValueOf(populated.normalized())
	gotType := got.Type()
	for i := range gotType.NumField() {
		field := gotType.Field(i)
		if tomlKeyOf(field) == "" {
			continue
		}
		if got.Field(i).IsZero() {
			t.Errorf("normalized() left Order.%s at its zero value: the decoded key is dropped", field.Name)
		}
	}
}

// tomlKeysOf returns the TOML keys a struct's exported fields decode from,
// skipping `toml:"-"` (set by the scanner, never parsed) and untagged fields.
func tomlKeysOf(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var keys []string
	for i := range typ.NumField() {
		if key := tomlKeyOf(typ.Field(i)); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func tomlKeyOf(field reflect.StructField) string {
	tag, ok := field.Tag.Lookup("toml")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return ""
	}
	return name
}
