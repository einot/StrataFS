package blobfs

// Root-pointer knowledge: whether the mount knows the current ETag of the root
// pointer, as a machine over one table, so that a write of the root pointer
// whose reply did not establish that ETag leaves the mount in doubt rather
// than holding an ETag it cannot trust. Only one read of the root pointer
// leaves InDoubt, and the read that shows another writer moves the mount mode
// instead, so no edge here is a divergence.
//
// Spec: docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md §3; docs/adr/0022-a-failed-root-swap-is-settled-by-one-read-of-the-root-pointer.md §2

import (
	"fmt"

	"strata/internal/fsm"
)

type rootState uint8

const (
	rootKnown   rootState = iota + 1 // FS.rootETag is the current ETag of the root pointer that names FS.rootSnapshot
	rootInDoubt                      // a write of the root pointer left its outcome, or the root pointer's ETag, unknown; FS.doubtKey names the attempt
)

type rootEvent uint8

const (
	rootSwapOK         rootEvent = iota + 1 // a write's reply gave the root pointer's ETag: Known to Known
	rootSwapUnknown                         // a write's reply did not: Known to InDoubt
	rootProbeOurs                           // the read showed the root pointer naming the attempt: InDoubt to Known
	rootProbeUnchanged                      // the read showed it naming the snapshot the namespace descends from: InDoubt to Known
)

var rootStates = fsm.NewTable("rootKnowledge",
	fsm.Edge[rootState, rootEvent]{From: rootKnown, On: rootSwapOK, To: rootKnown},
	fsm.Edge[rootState, rootEvent]{From: rootKnown, On: rootSwapUnknown, To: rootInDoubt},
	fsm.Edge[rootState, rootEvent]{From: rootInDoubt, On: rootProbeOurs, To: rootKnown},
	fsm.Edge[rootState, rootEvent]{From: rootInDoubt, On: rootProbeUnchanged, To: rootKnown})

func (s rootState) String() string {
	switch s {
	case rootKnown:
		return "Known"
	case rootInDoubt:
		return "InDoubt"
	}
	return fmt.Sprintf("rootState(%d)", uint8(s))
}

func (e rootEvent) String() string {
	switch e {
	case rootSwapOK:
		return "SwapOK"
	case rootSwapUnknown:
		return "SwapUnknown"
	case rootProbeOurs:
		return "ProbeOurs"
	case rootProbeUnchanged:
		return "ProbeUnchanged"
	}
	return fmt.Sprintf("rootEvent(%d)", uint8(e))
}
