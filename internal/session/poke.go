package session

import (
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// Durable poke record: the session-bead half of the cross-process activity
// discount. internal/runtime/poke.go owns the decision; this file owns getting
// the two timestamps from the process that sent the keystrokes to the process
// that later decides the session is idle.
//
// Why the bead and not a file beside the socket: the record has exactly one
// natural owner (the session), it must survive the sending CLI process by
// definition, and the reconciler is already holding the session's Info when it
// runs the idle check. A sidecar file would be a status file -- stale on crash,
// with nothing to reconcile it against.

// StampPokePatch returns the metadata patch recording one send-keys poke.
//
// Callers MUST only call this for a delivery that actually sent keystrokes.
// There is deliberately no zero-value-tolerant variant: a patch stamped for a
// hook-injected or ACP delivery would discount activity that never came from
// gc, which suppresses idle detection instead of restoring it.
func StampPokePatch(pk runtime.Poke) MetadataPatch {
	return MetadataPatch{
		MetadataLastPokeAt:            pk.At.UTC().Format(time.RFC3339),
		MetadataLastPokePriorActivity: pk.Prior.UTC().Format(time.RFC3339),
	}
}

// DurablePoke reconstructs the poke recorded on this session's bead. An
// incomplete record (either half missing or unparsable) yields a Poke that
// declines to discount anything -- see runtime.Poke.
func (i Info) DurablePoke() runtime.Poke {
	return runtime.Poke{At: i.LastPokeAt, Prior: i.LastPokePriorActivity}
}
