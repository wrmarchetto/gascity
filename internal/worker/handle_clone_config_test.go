package worker

import (
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

// Scope: cloneRuntimeConfig's completeness, and nothing else in this package.
// The clone feeds worker.Handle's config, whose CoreFingerprint is compared
// against the hash the start path stored -- so a reference field the clone
// forgets is not merely aliased, it is a hash that disagrees for the same
// session.
//
// It is a reflection walk rather than a list of fields because a hand-kept list
// is the thing that rots: runtime.Config gained DeclaredEnvKeys in this change,
// the clone needed a line for it, and nothing in this package would have failed
// had the line been missed -- handle_clone_test.go covers only profileFamily.
//
// Run: go test ./internal/worker/ -run CloneRuntimeConfig
func TestCloneRuntimeConfigDeepCopiesEveryReferenceField(t *testing.T) {
	// A value with every reference-typed field non-empty, built by reflection so
	// a NEW field is populated automatically rather than silently skipped. A
	// field left at its zero value cannot show aliasing, so an unpopulated one
	// would pass this test without being cloned at all.
	var cfg runtime.Config
	v := reflect.ValueOf(&cfg).Elem()
	typ := v.Type()
	populated := 0
	for i := 0; i < typ.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
			populated++
		case reflect.Map:
			f.Set(reflect.MakeMapWithSize(f.Type(), 0))
			populated++
		}
	}
	if populated == 0 {
		t.Fatal("populated no slice or map field; the reflection walk no longer " +
			"matches runtime.Config and this test proves nothing")
	}

	clone := cloneRuntimeConfig(cfg)
	cv := reflect.ValueOf(clone)

	checked := 0
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		orig, cloned := v.Field(i), cv.Field(i)
		switch orig.Kind() {
		case reflect.Slice:
			if orig.Len() == 0 {
				continue
			}
			checked++
			// Same backing array means the clone shares storage with its source.
			// A normalizing clone may legitimately return a different length, so
			// compare the pointer, not the contents.
			if cloned.Len() > 0 && orig.UnsafePointer() == cloned.UnsafePointer() {
				t.Errorf("Config.%s: clone shares its backing array with the source; "+
					"add it to cloneRuntimeConfig", name)
			}
		case reflect.Map:
			checked++
			if cloned.Len() == 0 && orig.Len() == 0 {
				// An empty map may clone to nil; that is not aliasing.
				continue
			}
			if orig.UnsafePointer() == cloned.UnsafePointer() {
				t.Errorf("Config.%s: clone shares its map with the source; "+
					"add it to cloneRuntimeConfig", name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked no field; the walk found nothing to verify")
	}
	t.Logf("checked %d reference fields on runtime.Config", checked)
}

// DeclaredEnvKeys specifically, because the reflection walk above proves only
// that the slice is not ALIASED -- a clone setting it to nil shares no backing
// array and passes. The field is a fingerprint input since v6, so losing it
// gives worker.Handle a hash that disagrees with the one the start path stored
// for the same session.
func TestCloneRuntimeConfigPreservesDeclaredEnvKeys(t *testing.T) {
	cfg := runtime.Config{
		Env:             map[string]string{"CLAUDE_ACCOUNTS": "0 4"},
		DeclaredEnvKeys: []string{"CLAUDE_ACCOUNTS"},
	}
	clone := cloneRuntimeConfig(cfg)

	if runtime.CoreFingerprint(clone) != runtime.CoreFingerprint(cfg) {
		t.Errorf("clone hashes differently from its source: %s vs %s",
			runtime.CoreFingerprint(clone), runtime.CoreFingerprint(cfg))
	}
	if len(clone.DeclaredEnvKeys) != 1 || clone.DeclaredEnvKeys[0] != "CLAUDE_ACCOUNTS" {
		t.Errorf("DeclaredEnvKeys = %v, want [CLAUDE_ACCOUNTS]", clone.DeclaredEnvKeys)
	}
}
