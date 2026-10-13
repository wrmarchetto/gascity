package main

// Doctor visibility for the managed Dolt maintenance hold (ci-lg7q7t).
//
// Deliberately has NO Fix. A doctor run that removed a hold would restart
// the server under the offline GC the hold protects, and `gc doctor --fix`
// runs unattended. Releasing is `gc maintenance release`, a decision only the
// holder or the operator makes.

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/doctor"
)

type doltMaintenanceHoldCheck struct {
	cityPath string
}

func newDoltMaintenanceHoldCheck(cityPath string) *doltMaintenanceHoldCheck {
	return &doltMaintenanceHoldCheck{cityPath: cityPath}
}

func (c *doltMaintenanceHoldCheck) Name() string                     { return "dolt-maintenance-hold" }
func (c *doltMaintenanceHoldCheck) CanFix() bool                     { return false }
func (c *doltMaintenanceHoldCheck) Fix(_ *doctor.CheckContext) error { return nil }
func (c *doltMaintenanceHoldCheck) WarmupEligible() bool             { return false }

// Run reports an active hold as an advisory warning -- it is intended, but
// every bead command in the city fails while it lasts -- and an unreadable
// hold file as a blocking error, because it refuses every server start with
// no expiry to end it. A lapsed file is inert and reported as OK.
func (c *doltMaintenanceHoldCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name()}
	hold, present, err := readManagedDoltMaintenanceHold(c.cityPath)
	path := managedDoltMaintenanceHoldPath(c.cityPath)
	switch {
	case !present:
		res.Status = doctor.StatusOK
		res.Message = "no managed Dolt maintenance hold"
	case err != nil:
		res.Status = doctor.StatusError
		res.Message = fmt.Sprintf("maintenance hold file %s is unreadable (%v); every managed Dolt start and recover refuses until it is removed", path, err)
		res.FixHint = "if no offline maintenance is running: gc maintenance release"
	case !managedDoltMaintenanceHoldNow().Before(hold.ExpiresAt):
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("maintenance hold by %s lapsed at %s and is inert", hold.Holder, hold.ExpiresAt.UTC().Format(time.RFC3339))
	default:
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = fmt.Sprintf("managed Dolt is held offline by %s until %s (%s); every bead command fails until then",
			hold.Holder, hold.ExpiresAt.UTC().Format(time.RFC3339), hold.Reason)
		res.FixHint = "when the maintenance is done: gc maintenance release"
	}
	return res
}
