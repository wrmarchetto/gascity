package main

// Unit suite for the dolt-maintenance-hold doctor check.
//
// Scope: the status each hold state maps to. The hold predicate itself is
// pinned in dolt_maintenance_hold_test.go, and this check must agree with it
// on every state, so each case also asserts the predicate's answer: a check
// reporting OK while the doors refuse is the misreport this guards against.
//
// Run: go test ./cmd/gc -run 'DoltMaintenanceHoldCheck' -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/doctor"
)

func TestDoltMaintenanceHoldCheckAgreesWithDoors(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, cityPath string) string // returns a token the message must carry
		lapsed     bool
		wantStatus doctor.CheckStatus
		wantRefuse bool
	}{
		{name: "absent", setup: func(*testing.T, string) string { return "no managed Dolt" }, wantStatus: doctor.StatusOK},
		{
			name: "active", setup: func(t *testing.T, p string) string { return writeHoldForTest(t, p).Reason },
			wantStatus: doctor.StatusWarning, wantRefuse: true,
		},
		{
			name: "lapsed", setup: func(t *testing.T, p string) string { writeHoldForTest(t, p); return "inert" },
			lapsed: true, wantStatus: doctor.StatusOK,
		},
		{name: "unreadable", setup: func(t *testing.T, p string) string {
			path := managedDoltMaintenanceHoldPath(p)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}, wantStatus: doctor.StatusError, wantRefuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityPath := t.TempDir()
			token := tc.setup(t, cityPath)
			if tc.lapsed {
				prev := managedDoltMaintenanceHoldNow
				t.Cleanup(func() { managedDoltMaintenanceHoldNow = prev })
				managedDoltMaintenanceHoldNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
			}
			res := newDoltMaintenanceHoldCheck(cityPath).Run(&doctor.CheckContext{CityPath: cityPath})
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %v, want %v (%s)", res.Status, tc.wantStatus, res.Message)
			}
			if !strings.Contains(res.Message, token) {
				t.Fatalf("message %q does not carry %q", res.Message, token)
			}
			if refused := managedDoltMaintenanceHoldRefusal(cityPath) != nil; refused != tc.wantRefuse {
				t.Fatalf("doors refuse = %v, want %v: check and doors disagree", refused, tc.wantRefuse)
			}
		})
	}
}
