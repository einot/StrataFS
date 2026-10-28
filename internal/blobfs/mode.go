package blobfs

// The mount mode: whether the mount accepts changes, as a machine over one
// table, so that its only legal move, Writable to Diverged on Diverge, can be
// listed and every other one is refused. ReadOnly and Diverged have no edge
// out, so no transition widens what the mount may do.
//
// Spec: docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md §3, §7

import (
	"fmt"

	"strata/internal/fsm"
)

type mountMode uint32

const (
	mountWritable mountMode = iota + 1 // accepts changes
	mountReadOnly                      // Config.ReadOnly; no edge leaves it
	mountDiverged                      // lost a swap to another writer; terminal
)

type mountEvent uint8

const mountDiverge mountEvent = 1 // a read of the root pointer showed another writer's root pointer, or none (ADR 0022 §3)

var mountModes = fsm.NewTable("mode",
	fsm.Edge[mountMode, mountEvent]{From: mountWritable, On: mountDiverge, To: mountDiverged})

func (m mountMode) String() string {
	switch m {
	case mountWritable:
		return "Writable"
	case mountReadOnly:
		return "ReadOnly"
	case mountDiverged:
		return "Diverged"
	}
	return fmt.Sprintf("mountMode(%d)", uint32(m))
}

func (e mountEvent) String() string {
	if e == mountDiverge {
		return "Diverge"
	}
	return fmt.Sprintf("mountEvent(%d)", uint8(e))
}
