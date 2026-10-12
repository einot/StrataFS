package fsm_test

// Clean-room tests for internal/fsm, written from ADR 0014,
// docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md,
// without reading the implementation. Section and item references are to that
// ADR:
//
//	§2  the fsm API, and every rule it pins
//	§3  the conventions a machine's types follow, which the test types here
//	    follow too: states start at 1, and states and events have String
//	    methods on value receivers
//	§11 the test surface, F1 to F12, which each test cites
//
// Every Machine and Atomic is held by the pointer its constructor returns and
// is never copied (§2). No test sleeps: the concurrent tests release their
// goroutines together with a start channel, wait for them with a
// sync.WaitGroup, have each goroutine record its results in its own slot, and
// assert only after the wait. They are meant to run under go test -race.

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"strata/internal/fsm"
)

// state is the tests' state type. Its underlying type is uint32, so that an
// fsm.Atomic can hold it (§2's S ~uint32), and its values start at 1, so that
// its zero value names no state (§3).
type state uint32

const (
	stA state = iota + 1
	stB
	stC
	stD
)

// String returns the bare name, or "state(<n>)" for a value with no name, as
// §3 asks, so that F7 can check §2's message exactly.
func (s state) String() string {
	switch s {
	case stA:
		return "A"
	case stB:
		return "B"
	case stC:
		return "C"
	case stD:
		return "D"
	}
	return "state(" + strconv.FormatUint(uint64(s), 10) + ")"
}

// event is the tests' event type. Its values also start at 1 (§3).
type event uint8

const (
	evGo event = iota + 1
	evBack
	evStay
	evNone
)

// String returns the bare name, or "event(<n>)" for a value with no name (§3).
func (e event) String() string {
	switch e {
	case evGo:
		return "Go"
	case evBack:
		return "Back"
	case evStay:
		return "Stay"
	case evNone:
		return "None"
	}
	return "event(" + strconv.FormatUint(uint64(e), 10) + ")"
}

// doorEdges returns, as a new slice on each call, the edges most tests use: A
// to B on Go, B to C on Go, B to A on Back, and C to C on Stay, a self-loop. No
// edge leaves D or names it, and no edge is on None.
func doorEdges() []fsm.Edge[state, event] {
	return []fsm.Edge[state, event]{
		{From: stA, On: evGo, To: stB},
		{From: stB, On: evGo, To: stC},
		{From: stB, On: evBack, To: stA},
		{From: stC, On: evStay, To: stC},
	}
}

// newDoor builds the table named "door" over doorEdges.
func newDoor() *fsm.Table[state, event] {
	return fsm.NewTable("door", doorEdges()...)
}

// someStates and someEvents are the pairs F1 and F4 ask Next about: every
// named value, the zero values, and a value with no name.
var (
	someStates = []state{0, stA, stB, stC, stD, state(99)}
	someEvents = []event{0, evGo, evBack, evStay, evNone, event(200)}
)

// edgeFor is the tests' own reading of §2's Next rule: the To of the edge in
// edges whose From and On are from and on, and true; or the zero state and
// false when there is none.
func edgeFor(edges []fsm.Edge[state, event], from state, on event) (state, bool) {
	for _, e := range edges {
		if e.From == from && e.On == on {
			return e.To, true
		}
	}
	return 0, false
}

// panicked runs f and reports whether it panicked, and with what value.
func panicked(f func()) (did bool, value any) {
	defer func() {
		if r := recover(); r != nil {
			did, value = true, r
		}
	}()
	f()
	return false, nil
}

// stateMachine is what Machine and Atomic share: §2 gives both State and Fire
// with the same signatures. A test holds in it the *Machine or *Atomic that
// the constructor returned, so nothing is copied.
type stateMachine interface {
	State() state
	Fire(on event) (from, to state, err error)
}

var (
	_ stateMachine = (*fsm.Machine[state, event])(nil)
	_ stateMachine = (*fsm.Atomic[state, event])(nil)
)

// builder makes a stateMachine over tbl that starts at initial.
type builder func(tbl *fsm.Table[state, event], initial state) stateMachine

func buildMachine(tbl *fsm.Table[state, event], initial state) stateMachine {
	return fsm.NewMachine(tbl, initial)
}

func buildAtomic(tbl *fsm.Table[state, event], initial state) stateMachine {
	return fsm.NewAtomic(tbl, initial)
}

// TestNextFollowsTheEdges covers F1 (ADR 0014 §11): Next returns (To, true)
// for each edge's (From, On), and (zero, false) for every other pair, the zero
// values included (§2).
func TestNextFollowsTheEdges(t *testing.T) {
	tbl := newDoor()
	oracle := doorEdges()
	for _, from := range someStates {
		for _, on := range someEvents {
			wantTo, wantOK := edgeFor(oracle, from, on)
			gotTo, gotOK := tbl.Next(from, on)
			if gotTo != wantTo || gotOK != wantOK {
				t.Errorf("Next(%v, %v) = (%v, %v), want (%v, %v): Next returns (To, true) for an edge's (From, On) and (zero S, false) for every other pair (ADR 0014 §2, §11 F1)",
					from, on, gotTo, gotOK, wantTo, wantOK)
			}
		}
	}
}

// TestNewTablePanicsOnADuplicateEdge covers F2 (ADR 0014 §11): NewTable
// panics on two edges with the same (From, On), whatever their To, and does
// not panic on edges that share only From or only On (§2).
func TestNewTablePanicsOnADuplicateEdge(t *testing.T) {
	duplicates := []struct {
		name  string
		edges []fsm.Edge[state, event]
	}{
		{name: "the same To", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stB}, {From: stA, On: evGo, To: stB}}},
		{name: "different To", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stB}, {From: stA, On: evGo, To: stC}}},
		{name: "not adjacent", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stB}, {From: stB, On: evGo, To: stC}, {From: stA, On: evGo, To: stD}}},
		{name: "a self-loop and an edge out", edges: []fsm.Edge[state, event]{{From: stC, On: evStay, To: stC}, {From: stC, On: evStay, To: stA}}},
		{name: "zero values", edges: []fsm.Edge[state, event]{{From: 0, On: 0, To: stA}, {From: 0, On: 0, To: stB}}},
	}
	for _, tc := range duplicates {
		edges := tc.edges
		if did, _ := panicked(func() { fsm.NewTable("dup", edges...) }); !did {
			t.Errorf("NewTable with two edges on the same (From, On), %s (%v), did not panic: a duplicate is a programming error, and NewTable panics on it whatever their To (ADR 0014 §2, §11 F2)",
				tc.name, edges)
		}
	}

	distinct := []struct {
		name  string
		edges []fsm.Edge[state, event]
	}{
		{name: "sharing only From", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stB}, {From: stA, On: evBack, To: stB}}},
		{name: "sharing only On", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stB}, {From: stB, On: evGo, To: stB}}},
		{name: "sharing only To", edges: []fsm.Edge[state, event]{{From: stA, On: evGo, To: stC}, {From: stB, On: evBack, To: stC}}},
		{name: "the door table", edges: doorEdges()},
	}
	for _, tc := range distinct {
		edges := tc.edges
		var tbl *fsm.Table[state, event]
		did, value := panicked(func() { tbl = fsm.NewTable("distinct", edges...) })
		if did {
			t.Errorf("NewTable with edges %s (%v) panicked with %v: edges that share only From, or only On, are fine (ADR 0014 §2, §11 F2)",
				tc.name, edges, value)
			continue
		}
		for _, e := range edges {
			if to, ok := tbl.Next(e.From, e.On); to != e.To || !ok {
				t.Errorf("NewTable with edges %s: Next(%v, %v) = (%v, %v), want (%v, true) (ADR 0014 §2, §11 F1)",
					tc.name, e.From, e.On, to, ok, e.To)
			}
		}
	}
}

// TestEdgesKeepsOrderAndIsACopy covers F3 (ADR 0014 §11): Edges keeps
// declaration order and returns a copy, and changing what it returned, or the
// slice passed to NewTable, changes neither a later Edges nor Next (§2).
func TestEdgesKeepsOrderAndIsACopy(t *testing.T) {
	// Not in any sorted order, so that only declaration order matches.
	given := []fsm.Edge[state, event]{
		{From: stC, On: evStay, To: stC},
		{From: stA, On: evGo, To: stB},
		{From: stB, On: evBack, To: stA},
		{From: stB, On: evGo, To: stC},
	}
	want := slices.Clone(given)
	tbl := fsm.NewTable("order", given...)

	first := tbl.Edges()
	if !slices.Equal(first, want) {
		t.Fatalf("Edges() = %v, want %v: Edges returns the edges in declaration order (ADR 0014 §2, §11 F3)", first, want)
	}

	// Change what Edges returned, and the slice passed to NewTable.
	first[0] = fsm.Edge[state, event]{From: stD, On: evNone, To: stD}
	first[len(first)-1].To = stA
	given[1].To = stD
	given[2] = fsm.Edge[state, event]{From: stD, On: evGo, To: stA}

	if second := tbl.Edges(); !slices.Equal(second, want) {
		t.Errorf("Edges() after changing an earlier Edges() result and the slice passed to NewTable = %v, want %v: Edges returns a new slice on each call, NewTable keeps its own copy, and changing either changes nothing in the table (ADR 0014 §2, §11 F3)",
			second, want)
	}
	for _, from := range someStates {
		for _, on := range someEvents {
			wantTo, wantOK := edgeFor(want, from, on)
			if gotTo, gotOK := tbl.Next(from, on); gotTo != wantTo || gotOK != wantOK {
				t.Errorf("Next(%v, %v) after changing an earlier Edges() result and the slice passed to NewTable = (%v, %v), want (%v, %v) (ADR 0014 §2, §11 F3)",
					from, on, gotTo, gotOK, wantTo, wantOK)
			}
		}
	}
}

// TestNameAndTheEmptyTable covers F4 (ADR 0014 §11): Name returns the name
// given, "" included, and a table with no edges, built with explicit type
// arguments, is valid: Next is (zero, false) for every pair, and Edges has
// length 0 (§2).
func TestNameAndTheEmptyTable(t *testing.T) {
	for _, name := range []string{"door", "mode", "", "a name with spaces"} {
		if got := fsm.NewTable(name, doorEdges()...).Name(); got != name {
			t.Errorf("NewTable(%q, ...).Name() = %q, want %q: Name returns the name NewTable was given, and NewTable accepts any name, \"\" included (ADR 0014 §2, §11 F4)",
				name, got, name)
		}
	}

	empty := fsm.NewTable[state, event]("empty")
	if got := empty.Name(); got != "empty" {
		t.Errorf("Name() of a table with no edges = %q, want %q (ADR 0014 §2, §11 F4)", got, "empty")
	}
	for _, from := range someStates {
		for _, on := range someEvents {
			if to, ok := empty.Next(from, on); to != 0 || ok {
				t.Errorf("Next(%v, %v) on a table with no edges = (%v, %v), want (%v, false) (ADR 0014 §2, §11 F4)",
					from, on, to, ok, state(0))
			}
		}
	}
	if got := empty.Edges(); len(got) != 0 {
		t.Errorf("Edges() of a table with no edges = %v, want length 0 (ADR 0014 §2, §11 F4)", got)
	}
}

// checkInitialState is F5 for one kind of machine: State is the initial state
// until a Fire succeeds, and a state that no edge names is a valid initial
// state (§2).
func checkInitialState(t *testing.T, kind string, build builder) {
	t.Helper()
	for _, initial := range []state{stA, stB, stC, stD} {
		m := build(newDoor(), initial)
		if got := m.State(); got != initial {
			t.Errorf("State() of a new %s started at %v = %v, want %v: until a Fire succeeds, State is the initial state, and the constructor does not check it against the table (ADR 0014 §2, §11 F5)",
				kind, initial, got, initial)
		}
	}
}

// checkLegalFire is F6 for one kind of machine: a legal Fire returns (from,
// to, nil) and moves the state to to, and a self-loop gives from == to (§2).
func checkLegalFire(t *testing.T, kind string, build builder) {
	t.Helper()
	m := build(newDoor(), stA)
	steps := []struct {
		on   event
		from state
		to   state
	}{
		{on: evGo, from: stA, to: stB},
		{on: evBack, from: stB, to: stA},
		{on: evGo, from: stA, to: stB},
		{on: evGo, from: stB, to: stC},
		{on: evStay, from: stC, to: stC},
		{on: evStay, from: stC, to: stC},
	}
	for i, s := range steps {
		from, to, err := m.Fire(s.on)
		if from != s.from || to != s.to || err != nil {
			t.Fatalf("%s step %d: Fire(%v) = (%v, %v, %v), want (%v, %v, nil): a legal Fire returns (current, to, nil), and a self-loop gives from == to (ADR 0014 §2, §11 F6)",
				kind, i, s.on, from, to, err, s.from, s.to)
		}
		if got := m.State(); got != s.to {
			t.Fatalf("%s step %d: State() after a legal Fire(%v) from %v = %v, want %v: a legal Fire sets the state to to (ADR 0014 §2, §11 F6)",
				kind, i, s.on, s.from, got, s.to)
		}
	}
}

// checkIllegalFire is F7 for one kind of machine (§2): an illegal Fire returns
// (cur, cur, err), err is an *IllegalError whose Machine, From and On are the
// table's name, the current state and the event, that unwraps to ErrIllegal,
// and whose Error() is exactly §2's text, and the state does not change.
func checkIllegalFire(t *testing.T, kind string, build builder) {
	t.Helper()
	cases := []struct {
		start state
		path  []event
		cur   state
		on    event
		text  string
	}{
		{start: stA, cur: stA, on: evBack, text: "fsm: door: illegal transition from A on Back"},
		{start: stA, cur: stA, on: evStay, text: "fsm: door: illegal transition from A on Stay"},
		{start: stA, cur: stA, on: evNone, text: "fsm: door: illegal transition from A on None"},
		{start: stA, cur: stA, on: 0, text: "fsm: door: illegal transition from A on event(0)"},
		{start: stA, cur: stA, on: event(200), text: "fsm: door: illegal transition from A on event(200)"},
		{start: stA, path: []event{evGo}, cur: stB, on: evStay, text: "fsm: door: illegal transition from B on Stay"},
		{start: stA, path: []event{evGo, evGo}, cur: stC, on: evGo, text: "fsm: door: illegal transition from C on Go"},
		{start: stD, cur: stD, on: evGo, text: "fsm: door: illegal transition from D on Go"},
	}
	for _, tc := range cases {
		m := build(newDoor(), tc.start)
		for _, on := range tc.path {
			if _, _, err := m.Fire(on); err != nil {
				t.Fatalf("%s setup: Fire(%v) = %v, want a legal transition of the door table", kind, on, err)
			}
		}

		from, to, err := m.Fire(tc.on)
		if from != tc.cur || to != tc.cur {
			t.Errorf("%s at %v: illegal Fire(%v) returned from %v and to %v, want both %v: an illegal Fire returns (current, current, err) (ADR 0014 §2, §11 F7)",
				kind, tc.cur, tc.on, from, to, tc.cur)
		}
		if got := m.State(); got != tc.cur {
			t.Errorf("%s at %v: State() after an illegal Fire(%v) = %v, want %v: an illegal Fire changes nothing (ADR 0014 §2, §11 F7)",
				kind, tc.cur, tc.on, got, tc.cur)
		}
		if err == nil {
			t.Errorf("%s at %v: illegal Fire(%v) returned a nil error, want an *fsm.IllegalError (ADR 0014 §2, §11 F7)", kind, tc.cur, tc.on)
			continue
		}
		if !errors.Is(err, fsm.ErrIllegal) {
			t.Errorf("%s at %v: errors.Is(Fire(%v)'s error %v, fsm.ErrIllegal) = false, want true: Unwrap returns ErrIllegal (ADR 0014 §2, §11 F7)",
				kind, tc.cur, tc.on, err)
		}
		var ie *fsm.IllegalError[state, event]
		if !errors.As(err, &ie) {
			t.Errorf("%s at %v: errors.As(Fire(%v)'s error %v, *fsm.IllegalError[state, event]) = false, want true (ADR 0014 §2, §11 F7)",
				kind, tc.cur, tc.on, err)
		} else {
			if ie.Machine != "door" || ie.From != tc.cur || ie.On != tc.on {
				t.Errorf("%s at %v: illegal Fire(%v) gave IllegalError{Machine: %q, From: %v, On: %v}, want {Machine: %q, From: %v, On: %v}: Machine is the table's Name(), From the current state and On the event fired (ADR 0014 §2, §11 F7)",
					kind, tc.cur, tc.on, ie.Machine, ie.From, ie.On, "door", tc.cur, tc.on)
			}
			if u := ie.Unwrap(); u != fsm.ErrIllegal {
				t.Errorf("%s at %v: IllegalError.Unwrap() = %v, want fsm.ErrIllegal (ADR 0014 §2)", kind, tc.cur, u)
			}
		}
		if got := err.Error(); got != tc.text {
			t.Errorf("%s at %v: illegal Fire(%v)'s Error() = %q, want exactly %q: \"fsm: \" + Machine + \": illegal transition from \" + fmt.Sprint(From) + \" on \" + fmt.Sprint(On), with a String method printing by it (ADR 0014 §2, §11 F7)",
				kind, tc.cur, tc.on, got, tc.text)
		}
	}
}

// TestMachineInitialState covers F5 (ADR 0014 §11) for Machine.
func TestMachineInitialState(t *testing.T) {
	checkInitialState(t, "Machine", buildMachine)
}

// TestMachineLegalFire covers F6 (ADR 0014 §11) for Machine.
func TestMachineLegalFire(t *testing.T) {
	checkLegalFire(t, "Machine", buildMachine)
}

// TestMachineIllegalFire covers F7 (ADR 0014 §11) for Machine.
func TestMachineIllegalFire(t *testing.T) {
	checkIllegalFire(t, "Machine", buildMachine)
}

// TestAtomicInitialState covers F8 (ADR 0014 §11): F5 for Atomic.
func TestAtomicInitialState(t *testing.T) {
	checkInitialState(t, "Atomic", buildAtomic)
}

// TestAtomicLegalFire covers F8 (ADR 0014 §11): F6 for Atomic.
func TestAtomicLegalFire(t *testing.T) {
	checkLegalFire(t, "Atomic", buildAtomic)
}

// TestAtomicIllegalFire covers F8 (ADR 0014 §11): F7 for Atomic.
func TestAtomicIllegalFire(t *testing.T) {
	checkIllegalFire(t, "Atomic", buildAtomic)
}

// TestIllegalErrorTextWithoutStringMethods covers F7's exact text (ADR 0014
// §11) for types with no String method, for which §2's fmt.Sprint prints the
// value itself, on a Machine and on an Atomic.
func TestIllegalErrorTextWithoutStringMethods(t *testing.T) {
	plain := fsm.NewTable("plain", fsm.Edge[int, string]{From: 1, On: "push", To: 2})
	m := fsm.NewMachine(plain, 1)
	from, to, err := m.Fire("pull")
	if from != 1 || to != 1 || err == nil {
		t.Fatalf("Machine at 1: illegal Fire(%q) = (%v, %v, %v), want (1, 1, an *fsm.IllegalError) (ADR 0014 §2, §11 F7)", "pull", from, to, err)
	}
	var pe *fsm.IllegalError[int, string]
	if !errors.As(err, &pe) || pe.Machine != "plain" || pe.From != 1 || pe.On != "pull" {
		t.Errorf("Machine at 1: illegal Fire(%q)'s error %v is not an *fsm.IllegalError[int, string] with Machine \"plain\", From 1 and On \"pull\" (ADR 0014 §2, §11 F7)", "pull", err)
	}
	if got, want := err.Error(), "fsm: plain: illegal transition from 1 on pull"; got != want {
		t.Errorf("Machine at 1: illegal Fire(%q)'s Error() = %q, want exactly %q (ADR 0014 §2, §11 F7)", "pull", got, want)
	}

	raw := fsm.NewTable("raw", fsm.Edge[uint32, int]{From: 1, On: 1, To: 2})
	a := fsm.NewAtomic(raw, 3)
	afrom, ato, aerr := a.Fire(-1)
	if afrom != 3 || ato != 3 || aerr == nil {
		t.Fatalf("Atomic at 3: illegal Fire(-1) = (%v, %v, %v), want (3, 3, an *fsm.IllegalError) (ADR 0014 §2, §11 F7, F8)", afrom, ato, aerr)
	}
	var ae *fsm.IllegalError[uint32, int]
	if !errors.As(aerr, &ae) || ae.Machine != "raw" || ae.From != 3 || ae.On != -1 {
		t.Errorf("Atomic at 3: illegal Fire(-1)'s error %v is not an *fsm.IllegalError[uint32, int] with Machine \"raw\", From 3 and On -1 (ADR 0014 §2, §11 F7, F8)", aerr)
	}
	if got, want := aerr.Error(), "fsm: raw: illegal transition from 3 on -1"; got != want {
		t.Errorf("Atomic at 3: illegal Fire(-1)'s Error() = %q, want exactly %q (ADR 0014 §2, §11 F7, F8)", got, want)
	}
	if got := a.State(); got != 3 {
		t.Errorf("Atomic at 3: State() after an illegal Fire = %v, want 3 (ADR 0014 §2, §11 F7, F8)", got)
	}
}

// fireOutcome is what one Fire returned.
type fireOutcome struct {
	from state
	to   state
	err  error
}

// TestAtomicOneWinnerAmongManyFires covers F9 (ADR 0014 §11): of 64
// goroutines released together on one Atomic over {A to B on Go}, exactly one
// Fire returns (A, B, nil), every other returns (B, B, err) with err an
// *IllegalError whose From is B, and the state ends at B; over 100 rounds,
// each with a new Atomic. §2: Fire is a compare-and-swap loop, so every Fire
// is linearizable and no transition is applied twice.
func TestAtomicOneWinnerAmongManyFires(t *testing.T) {
	oneShot := fsm.NewTable("oneShot", fsm.Edge[state, event]{From: stA, On: evGo, To: stB})
	const rounds, firers = 100, 64
	for round := range rounds {
		a := fsm.NewAtomic(oneShot, stA)
		outs := make([]fireOutcome, firers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range firers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				from, to, err := a.Fire(evGo)
				outs[i] = fireOutcome{from: from, to: to, err: err}
			}(i)
		}
		close(start)
		wg.Wait()

		failed := false
		winners := 0
		for i, o := range outs {
			if o.from == stA && o.to == stB && o.err == nil {
				winners++
				continue
			}
			var ie *fsm.IllegalError[state, event]
			if o.from != stB || o.to != stB || !errors.As(o.err, &ie) || ie.From != stB || ie.On != evGo || ie.Machine != "oneShot" {
				t.Errorf("round %d: Fire %d of %d concurrent Fire(Go) calls on {A to B on Go} = (%v, %v, %v), want (A, B, nil), or (B, B, an *fsm.IllegalError with Machine \"oneShot\", From B and On Go): every Fire is linearizable (ADR 0014 §2, §11 F9)",
					round, i, firers, o.from, o.to, o.err)
				failed = true
			}
		}
		if winners != 1 {
			t.Errorf("round %d: %d of %d concurrent Fire(Go) calls on {A to B on Go} returned (A, B, nil), want exactly 1: no transition is applied twice (ADR 0014 §2, §11 F9)",
				round, winners, firers)
			failed = true
		}
		if got := a.State(); got != stB {
			t.Errorf("round %d: State() after %d concurrent Fire(Go) calls on {A to B on Go} = %v, want B (ADR 0014 §2, §11 F9)", round, firers, got)
			failed = true
		}
		if failed {
			return
		}
	}
}

// TestAtomicTwoFiresTakeTwoSteps covers F10 (ADR 0014 §11): on {A to B, B to
// C}, both on Go, two concurrent Fire calls on one Atomic return (A, B, nil)
// and (B, C, nil), one each, and the state ends at C; over 100 rounds. §2: a
// Fire retries when another Fire changed the state in between, so no
// transition is lost.
func TestAtomicTwoFiresTakeTwoSteps(t *testing.T) {
	chain := fsm.NewTable("chain",
		fsm.Edge[state, event]{From: stA, On: evGo, To: stB},
		fsm.Edge[state, event]{From: stB, On: evGo, To: stC},
	)
	wantAB := fireOutcome{from: stA, to: stB}
	wantBC := fireOutcome{from: stB, to: stC}
	for round := range 100 {
		a := fsm.NewAtomic(chain, stA)
		var outs [2]fireOutcome
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				from, to, err := a.Fire(evGo)
				outs[i] = fireOutcome{from: from, to: to, err: err}
			}(i)
		}
		close(start)
		wg.Wait()

		ok := (outs[0] == wantAB && outs[1] == wantBC) || (outs[0] == wantBC && outs[1] == wantAB)
		if !ok {
			t.Errorf("round %d: two concurrent Fire(Go) calls on {A to B, B to C} returned (%v, %v, %v) and (%v, %v, %v), want (A, B, nil) and (B, C, nil), one each: a Fire that loses a race retries from the new state (ADR 0014 §2, §11 F10)",
				round, outs[0].from, outs[0].to, outs[0].err, outs[1].from, outs[1].to, outs[1].err)
		}
		if got := a.State(); got != stC {
			t.Errorf("round %d: State() after two concurrent Fire(Go) calls on {A to B, B to C} = %v, want C (ADR 0014 §2, §11 F10)", round, got)
		}
		if t.Failed() {
			return
		}
	}
}

// TestAtomicStateAndFireConcurrently covers F11 (ADR 0014 §11): State and
// Fire, called concurrently on one Atomic, are race-free under go test -race,
// and every State returned is one the table can reach. The table is a ring, A
// to B to C to A on Go, so every Fire(Go) is legal and every Fire(Back) is
// not. Beyond F11, it also checks §2's rules on what each Fire returned, and
// that, since no transition is lost or applied twice, the final state is A
// moved round the ring once per successful Fire.
func TestAtomicStateAndFireConcurrently(t *testing.T) {
	ringEdges := []fsm.Edge[state, event]{
		{From: stA, On: evGo, To: stB},
		{From: stB, On: evGo, To: stC},
		{From: stC, On: evGo, To: stA},
	}
	ring := fsm.NewTable("ring", ringEdges...)
	reachable := map[state]bool{stA: true, stB: true, stC: true}

	type fireRecord struct {
		on   event
		from state
		to   state
		err  error
	}
	const firers, readers, perFirer, perReader = 8, 8, 400, 400
	a := fsm.NewAtomic(ring, stA)
	fires := make([][]fireRecord, firers)
	reads := make([][]state, readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range firers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			recs := make([]fireRecord, 0, perFirer)
			for j := range perFirer {
				on := evGo
				if j%4 == 3 {
					on = evBack
				}
				from, to, err := a.Fire(on)
				recs = append(recs, fireRecord{on: on, from: from, to: to, err: err})
			}
			fires[i] = recs
		}(i)
	}
	for i := range readers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got := make([]state, 0, perReader)
			for range perReader {
				got = append(got, a.State())
			}
			reads[i] = got
		}(i)
	}
	close(start)
	wg.Wait()

	bad := 0
	report := func(format string, args ...any) {
		t.Helper()
		bad++
		if bad <= 10 {
			t.Errorf(format, args...)
		}
	}
	for i, got := range reads {
		for j, s := range got {
			if !reachable[s] {
				report("reader %d, read %d: State() = %v, want a state the ring table can reach from A (A, B or C) (ADR 0014 §2, §11 F11)", i, j, s)
			}
		}
	}
	successes := 0
	for i, recs := range fires {
		for j, r := range recs {
			if r.on == evGo {
				next, _ := edgeFor(ringEdges, r.from, evGo)
				if r.err != nil || !reachable[r.from] || r.to != next {
					report("firer %d, Fire %d: Fire(Go) on the ring = (%v, %v, %v), want (s, the next state after s, nil) for a reachable s: every state of the ring has an edge on Go (ADR 0014 §2, §11 F11)",
						i, j, r.from, r.to, r.err)
					continue
				}
				successes++
				continue
			}
			if r.err == nil || !errors.Is(r.err, fsm.ErrIllegal) || r.from != r.to || !reachable[r.from] {
				report("firer %d, Fire %d: Fire(Back) on the ring = (%v, %v, %v), want (s, s, an error wrapping fsm.ErrIllegal) for a reachable s: no state of the ring has an edge on Back (ADR 0014 §2, §11 F11)",
					i, j, r.from, r.to, r.err)
			}
		}
	}
	if bad > 10 {
		t.Errorf("... and %d more failures like those above", bad-10)
	}
	order := []state{stA, stB, stC}
	if got, want := a.State(), order[successes%len(order)]; got != want {
		t.Errorf("State() after %d successful concurrent Fire(Go) calls round the ring from A = %v, want %v: every Fire is linearizable, and no transition is lost or applied twice (ADR 0014 §2)",
			successes, got, want)
	}
}

// TestConstructorsPanicOnANilTable covers F12 (ADR 0014 §11): NewMachine and
// NewAtomic panic if the table is nil (§2).
func TestConstructorsPanicOnANilTable(t *testing.T) {
	if did, _ := panicked(func() { fsm.NewMachine[state, event](nil, stA) }); !did {
		t.Errorf("NewMachine(nil, A) did not panic, want a panic: NewMachine panics if t is nil (ADR 0014 §2, §11 F12)")
	}
	if did, _ := panicked(func() { fsm.NewAtomic[state, event](nil, stA) }); !did {
		t.Errorf("NewAtomic(nil, A) did not panic, want a panic: NewAtomic panics if t is nil (ADR 0014 §2, §11 F12)")
	}
}
