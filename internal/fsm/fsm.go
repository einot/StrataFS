// Package fsm holds state machines as explicit tables of edges, so that every
// legal transition of a machine can be listed and compared with a spec's Next
// relation, and every other one is refused.
//
// A Table is built once from constants and never changes. A Machine walks a
// table under whatever lock its owner holds; an Atomic walks one in a single
// atomic word, for state read without the lock its writers hold. Neither
// logs: an illegal transition is an error, which the owner logs and returns.
//
// Spec: docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md §2, §4
package fsm

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
)

// Edge is one legal transition: from From, on On, to To.
type Edge[S, E comparable] struct {
	From S
	On   E
	To   S
}

// key is the (from, on) pair a table looks an edge up by.
type key[S, E comparable] struct {
	from S
	on   E
}

// Table is a machine's edges. It never changes after NewTable returns, so it is
// safe for concurrent use.
type Table[S, E comparable] struct {
	name  string
	edges []Edge[S, E]
	next  map[key[S, E]]S
}

// NewTable builds a table named name from edges, keeping its own copy of them
// in the order given. Tables are built once from constants, so two edges with
// the same From and On are a programming error, and NewTable panics on them
// whatever their To.
func NewTable[S, E comparable](name string, edges ...Edge[S, E]) *Table[S, E] {
	t := &Table[S, E]{
		name:  name,
		edges: slices.Clone(edges),
		next:  make(map[key[S, E]]S, len(edges)),
	}
	for _, e := range t.edges {
		k := key[S, E]{from: e.From, on: e.On}
		if _, dup := t.next[k]; dup {
			panic(fmt.Sprintf("fsm: %s: two edges from %v on %v", name, e.From, e.On))
		}
		t.next[k] = e.To
	}
	return t
}

// Name returns the name NewTable was given.
func (t *Table[S, E]) Name() string {
	return t.name
}

// Next returns the To of the edge from from on on, and true; or the zero S and
// false when there is no such edge.
func (t *Table[S, E]) Next(from S, on E) (S, bool) {
	to, ok := t.next[key[S, E]{from: from, on: on}]
	return to, ok
}

// Edges returns the table's edges in declaration order, in a new slice on each
// call, so that changing it changes nothing in the table.
func (t *Table[S, E]) Edges() []Edge[S, E] {
	return slices.Clone(t.edges)
}

// ErrIllegal is what every IllegalError unwraps to.
var ErrIllegal = errors.New("fsm: illegal transition")

// IllegalError reports a Fire with no edge from the current state on the
// event fired.
type IllegalError[S, E comparable] struct {
	Machine string
	From    S
	On      E
}

func (e *IllegalError[S, E]) Error() string {
	return "fsm: " + e.Machine + ": illegal transition from " + fmt.Sprint(e.From) + " on " + fmt.Sprint(e.On)
}

// Unwrap returns ErrIllegal, so that errors.Is(err, ErrIllegal) holds.
func (e *IllegalError[S, E]) Unwrap() error {
	return ErrIllegal
}

// noCopy makes go vet's copylocks check report a copy of the struct that holds
// it. It is held as a named field, never embedded, so that its Lock and Unlock
// are not promoted to the holder.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Machine is a state walked through a table. It takes no lock and is not safe
// for concurrent use: its owner guards it with the lock that guards the rest
// of what it owns. The zero Machine is not usable; NewMachine makes one.
type Machine[S, E comparable] struct {
	noCopy noCopy
	table  *Table[S, E]
	state  S
}

// NewMachine returns a machine over t in state initial. It panics if t is nil.
// initial is not checked against t, since a terminal or read-only state that
// no edge names is a valid place to start.
func NewMachine[S, E comparable](t *Table[S, E], initial S) *Machine[S, E] {
	if t == nil {
		panic("fsm: NewMachine with a nil table")
	}
	return &Machine[S, E]{table: t, state: initial}
}

// State returns the current state.
func (m *Machine[S, E]) State() S {
	return m.state
}

// Fire moves the machine along the edge from its current state on on, and
// returns the state it left and the state it entered. When there is no such
// edge it changes nothing and returns the current state twice, with an
// *IllegalError.
func (m *Machine[S, E]) Fire(on E) (from, to S, err error) {
	cur := m.state
	next, ok := m.table.Next(cur, on)
	if !ok {
		return cur, cur, &IllegalError[S, E]{Machine: m.table.name, From: cur, On: on}
	}
	m.state = next
	return cur, next, nil
}

// Atomic is a state walked through a table in one atomic word, for state read
// without the lock its writers hold. Its states are ~uint32 so that the word
// holds them. The zero Atomic is not usable; NewAtomic makes one.
type Atomic[S ~uint32, E comparable] struct {
	table *Table[S, E]
	state atomic.Uint32
}

// NewAtomic returns an atomic machine over t in state initial. It panics if t
// is nil, and does not check initial against t.
func NewAtomic[S ~uint32, E comparable](t *Table[S, E], initial S) *Atomic[S, E] {
	if t == nil {
		panic("fsm: NewAtomic with a nil table")
	}
	a := &Atomic[S, E]{table: t}
	a.state.Store(uint32(initial))
	return a
}

// State returns the current state, in one atomic load.
func (a *Atomic[S, E]) State() S {
	return S(a.state.Load())
}

// Fire is Machine's Fire, made linearizable by a compare-and-swap loop: it
// swaps only from the state it loaded, and when another Fire changed the state
// in between it looks the edge up again from the new one. So no transition is
// lost or applied twice, and a retry happens only after another Fire has
// succeeded.
func (a *Atomic[S, E]) Fire(on E) (from, to S, err error) {
	for {
		cur := S(a.state.Load())
		next, ok := a.table.Next(cur, on)
		if !ok {
			return cur, cur, &IllegalError[S, E]{Machine: a.table.name, From: cur, On: on}
		}
		if a.state.CompareAndSwap(uint32(cur), uint32(next)) {
			return cur, next, nil
		}
	}
}
