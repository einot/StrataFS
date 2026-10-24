package blobfs

// Clean-room tests of prunable, the decision function ADR 0019 pins for #88,
// written without reading the implementation, from
// docs/adr/0019-a-prune-pass-deletes-only-below-its-own-commit.md section 2
// (prunable's contract) and section 6 (P1 to P6, and the snapshot-key format
// under "Names a test may use").
//
// The contract, ADR 0019 section 2: prunable(keys, current, keep) returns the
// keys a pass begun for current may delete. Let below be every element of
// keys less than current in Go string order; a keep below 0 counts as 0; if
// below has at most keep elements the result has length 0; otherwise it is
// below sorted ascending with its last keep elements removed. It modifies
// neither keys nor its elements, and keys are taken as distinct. Only the
// result's length and contents are pinned, not whether an empty one is nil,
// so every comparison here is slices.Equal, which treats the two alike.
//
// The oracle, prnOracle, is written from that paragraph and nothing else.
// The tests use no names of the package but prunable and snapshotPrefix: no
// FS, no store, no goroutine and no sleep. A panic out of prunable is
// recovered and reported as a failure of the case that caused it.
//
// How P3 and P6 catch two wrong implementations. Both are written out below
// as mutants, prnSparesCurrentEpoch and prnKeepsWholeListing, and P3 and P6
// each fail, as a defect of the test itself, if their inputs do not tell
// either mutant apart from the right answer; so the claims here are checked,
// not just stated.
//
//   - One that spares only the current epoch (the rule at 13aebc4, #88): it
//     keeps the newest keep keys of the whole listing, and deletes the rest
//     except those at current's epoch. P3 catches it twice over. With keys
//     above current that fall outside the newest keep, it returns them, and
//     P3's "current and every key above it are never in the result" cases
//     want them absent (e.g. keys e2-lo e3-lo e3-hi e4-lo e4-hi e5-lo,
//     current e3-lo, keep 0: it returns e2-lo e4-lo e4-hi e5-lo, P3 wants
//     e2-lo). And it spares a key at current's epoch with a lower nonce,
//     which P3's second clause says is below current and so is deleted when
//     keep allows (keys e2-lo e3-lo e3-hi e4-lo, current e3-hi, keep 0: it
//     returns e2-lo e4-lo, P3 wants e2-lo e3-lo). P6 catches it on every
//     input where a key at a higher epoch than current's lies outside the
//     whole listing's newest keep, for instance keys {e2-lo, e3-lo, e4-hi},
//     current e2-lo, keep 1: it returns e3-lo, the oracle nothing.
//   - One that keeps retention keys of the whole listing: it keeps the newest
//     keep keys of all of keys, current and those above it included, then
//     deletes the rest that sort below current. It never returns current or
//     a key above it, so a P3 that only checked membership would pass it;
//     P3's cases assert the exact result instead, and in those where keys at
//     or above current take up the window, it deletes keys below current
//     that the contract keeps (keys e2-lo e2-hi e3-lo e4-lo e4-hi, current
//     e3-hi, keep 1: it returns e2-lo e2-hi e3-lo, P3 wants e2-lo e2-hi). P6
//     catches it wherever keep is at least 1 and a subset holds a key at or
//     above current and more than keep keys below it, e.g. keys {e2-lo,
//     e2-hi, e4-lo},
//     current e3-lo, keep 1: it returns e2-lo e2-hi, the oracle e2-lo.
//
// A third mutant, prnPrunesByEpoch, takes below by epoch rather than by key
// (the alternative ADR 0019 Assumption 3 rejects); P3's second clause and P6
// catch it too, and are held to that in the same way.
//
// Not covered, on purpose: that prunable is pure and is what pruneSnapshots
// deletes by, which ADR 0019 section 6 leaves to review; keys that are not
// snapshot keys, or a current that is not one, which section 2 does not
// address; duplicate keys, which section 2 rules out; and whether the result
// shares keys' backing array, which section 2 does not pin.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// prnLo and prnHi are the two nonces the tests' keys use: twelve lower-case
// hex digits each, prnLo sorting below prnHi.
const (
	prnLo = "0000000000a1"
	prnHi = "fedcba987654"
)

// prnSeed is P6's fixed seed for shuffling each subset. Every P6 failure
// prints it.
const prnSeed uint64 = 0x88_0019

// prnMaxReports is how many failing inputs P2 and P6 report one by one before
// they only count the rest.
const prnMaxReports = 20

// prnKey is the snapshot key of epoch and nonce, in the format ADR 0019
// section 6 restates: snapshotPrefix + fmt.Sprintf("%020d-%s.json.gz", epoch,
// nonce).
func prnKey(epoch uint64, nonce string) string {
	return snapshotPrefix + fmt.Sprintf("%020d-%s.json.gz", epoch, nonce)
}

// prnParse splits a snapshot key into its epoch and nonce.
func prnParse(key string) (epoch uint64, nonce string, ok bool) {
	name, found := strings.CutPrefix(key, snapshotPrefix)
	if !found || len(name) != 20+1+12+len(".json.gz") || name[20] != '-' || !strings.HasSuffix(name, ".json.gz") {
		return 0, "", false
	}
	e, err := strconv.ParseUint(name[:20], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return e, name[21:33], true
}

// prnShort names a key briefly for a message: "e3-lo" for the key at epoch 3
// with nonce prnLo, or the quoted key if it is not a snapshot key.
func prnShort(key string) string {
	e, nonce, ok := prnParse(key)
	if !ok {
		return strconv.Quote(key)
	}
	switch nonce {
	case prnLo:
		nonce = "lo"
	case prnHi:
		nonce = "hi"
	}
	return fmt.Sprintf("e%d-%s", e, nonce)
}

// prnShowAll names a list of keys briefly for a message.
func prnShowAll(keys []string) string {
	short := make([]string, len(keys))
	for i, k := range keys {
		short[i] = prnShort(k)
	}
	return "[" + strings.Join(short, " ") + "]"
}

// prnOracle is ADR 0019 section 2's contract, transcribed: the keys below
// current in string order, sorted ascending, with the last max(keep, 0)
// removed, or nothing if there are no more than that.
func prnOracle(keys []string, current string, keep int) []string {
	var below []string
	for _, k := range keys {
		if k < current {
			below = append(below, k)
		}
	}
	slices.Sort(below)
	keep = max(keep, 0)
	if len(below) <= keep {
		return nil
	}
	return below[:len(below)-keep]
}

// prnSparesCurrentEpoch is a wrong implementation: the rule at 13aebc4. It
// keeps the newest keep keys of the whole listing and dooms every other one
// whose epoch is not current's.
func prnSparesCurrentEpoch(keys []string, current string, keep int) []string {
	keep = max(keep, 0)
	if len(keys) <= keep {
		return nil
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	ce, _, _ := prnParse(current)
	var out []string
	for _, k := range sorted[:len(sorted)-keep] {
		if e, _, _ := prnParse(k); e != ce {
			out = append(out, k)
		}
	}
	return out
}

// prnKeepsWholeListing is a wrong implementation: it keeps the newest keep
// keys of the whole listing, then dooms the rest that sort below current.
func prnKeepsWholeListing(keys []string, current string, keep int) []string {
	keep = max(keep, 0)
	if len(keys) <= keep {
		return nil
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	var out []string
	for _, k := range sorted[:len(sorted)-keep] {
		if k < current {
			out = append(out, k)
		}
	}
	return out
}

// prnPrunesByEpoch is a wrong implementation: it takes below as the keys at
// an epoch lower than current's, so a key at current's epoch with a lower
// nonce is never doomed.
func prnPrunesByEpoch(keys []string, current string, keep int) []string {
	ce, _, _ := prnParse(current)
	var below []string
	for _, k := range keys {
		if e, _, _ := prnParse(k); e < ce {
			below = append(below, k)
		}
	}
	slices.Sort(below)
	keep = max(keep, 0)
	if len(below) <= keep {
		return nil
	}
	return below[:len(below)-keep]
}

// prnMutants are the wrong implementations P3 and P6 must each tell apart
// from the contract.
var prnMutants = []struct {
	name string
	fn   func(keys []string, current string, keep int) []string
}{
	{"one that spares only the current epoch (prnSparesCurrentEpoch)", prnSparesCurrentEpoch},
	{"one that keeps retention keys of the whole listing (prnKeepsWholeListing)", prnKeepsWholeListing},
	{"one that takes below by epoch rather than by key (prnPrunesByEpoch)", prnPrunesByEpoch},
}

// prnCall calls prunable, recovering a panic out of it.
func prnCall(keys []string, current string, keep int) (got []string, panicked bool, pval any) {
	defer func() {
		if r := recover(); r != nil {
			panicked, pval = true, r
		}
	}()
	return prunable(keys, current, keep), false, nil
}

// prnDiagnose says which of the contract's properties got breaks, for a
// message, or "" if none of those it looks for.
func prnDiagnose(got []string, current string) string {
	var notes []string
	for _, k := range got {
		if k >= current {
			notes = append(notes, fmt.Sprintf("it holds %s, which does not sort below current %s, and a pass never "+
				"deletes current or a key above it (P3)", prnShort(k), prnShort(current)))
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			notes = append(notes, "it is not strictly ascending (P2)")
			break
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, "; ") + ")"
}

// prnProblem calls prunable(keys, current, keep) and says how it differs from
// want, or from leaving keys unchanged (P5), or "" if it does not.
func prnProblem(keys []string, current string, keep int, want []string) string {
	before := slices.Clone(keys)
	got, panicked, pval := prnCall(keys, current, keep)
	var msgs []string
	if panicked {
		msgs = append(msgs, fmt.Sprintf("it panicked with %v", pval))
	}
	if !slices.Equal(keys, before) {
		msgs = append(msgs, fmt.Sprintf("it changed keys to %s (P5: keys is unchanged by the call)", prnShowAll(keys)))
	}
	if !panicked && !slices.Equal(got, want) {
		msgs = append(msgs, fmt.Sprintf("it returned %s, want %s%s", prnShowAll(got), prnShowAll(want),
			prnDiagnose(got, current)))
	}
	if len(msgs) == 0 {
		return ""
	}
	return fmt.Sprintf("prunable(%s, %s, %d): %s (ADR 0019 section 2)", prnShowAll(before), prnShort(current), keep,
		strings.Join(msgs, "; "))
}

// prnCase is one input to prunable and the result ADR 0019 section 2 gives
// for it, written out by hand.
type prnCase struct {
	name    string
	keys    []string
	current string
	keep    int
	want    []string
}

// prnRun runs each case as a subtest.
func prnRun(t *testing.T, cases []prnCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if p := prnProblem(c.keys, c.current, c.keep, c.want); p != "" {
				t.Error(p)
			}
		})
	}
}

// prnPermute calls fn with a copy of every permutation of s, by Heap's
// algorithm.
func prnPermute(s []string, fn func([]string)) {
	var gen func(k int)
	gen = func(k int) {
		if k <= 1 {
			fn(slices.Clone(s))
			return
		}
		gen(k - 1)
		for i := 0; i < k-1; i++ {
			if k%2 == 0 {
				s[i], s[k-1] = s[k-1], s[i]
			} else {
				s[0], s[k-1] = s[k-1], s[0]
			}
			gen(k - 1)
		}
	}
	gen(len(s))
}

// TestPrunableDecidesWhatAPassDeletes is ADR 0019 section 6's "decision
// function" test, P1 to P6, of prunable as section 2 specifies it.
func TestPrunableDecidesWhatAPassDeletes(t *testing.T) {
	e1hi := prnKey(1, prnHi)
	e2lo, e2hi := prnKey(2, prnLo), prnKey(2, prnHi)
	e3lo, e3hi := prnKey(3, prnLo), prnKey(3, prnHi)
	e4lo, e4hi := prnKey(4, prnLo), prnKey(4, prnHi)
	e5lo := prnKey(5, prnLo)
	s := func(keys ...string) []string { return keys }

	// P1. No key below current, or at most keep of them: a result of length 0.
	t.Run("P1", func(t *testing.T) {
		prnRun(t, []prnCase{
			{"nil keys, keep 0", nil, e3lo, 0, nil},
			{"nil keys, keep -1", nil, e3lo, -1, nil},
			{"nil keys, keep 1", nil, e3lo, 1, nil},
			{"empty keys, keep 0", []string{}, e3lo, 0, nil},
			{"only current, keep 0", s(e3lo), e3lo, 0, nil},
			{"current and keys above it, keep 0", s(e4hi, e3hi, e5lo, e3lo), e3lo, 0, nil},
			{"current and keys above it, keep -1", s(e4hi, e3hi, e5lo, e3lo), e3lo, -1, nil},
			{"current and keys above it, keep 1", s(e4hi, e3hi, e5lo, e3lo), e3lo, 1, nil},
			{"every key above a current not among them, keep 0", s(e2lo, e4lo, e2hi), e1hi, 0, nil},
			{"as many below as keep", s(e3hi, e2lo, e4lo, e2hi), e3lo, 2, nil},
			{"fewer below than keep", s(e3hi, e2lo, e4lo, e2hi), e3lo, 3, nil},
			{"fewer below than a keep of math.MaxInt", s(e3hi, e2lo, e4lo, e2hi), e3lo, math.MaxInt, nil},
			{"one below and keep 1", s(e2hi, e3lo), e3lo, 1, nil},
		})
	})

	// P2. More than keep below: the oldest len(below) - keep, ascending,
	// whatever order keys came in. Every order of six keys, four of them
	// below current, one current and one above it.
	t.Run("P2", func(t *testing.T) {
		wants := map[int][]string{
			1: s(e2lo, e2hi, e3lo),
			2: s(e2lo, e2hi),
			3: s(e2lo),
		}
		seen := make(map[string]bool)
		reports := 0
		prnPermute(s(e2lo, e2hi, e3lo, e3hi, e4lo, e5lo), func(keys []string) {
			seen[strings.Join(keys, ",")] = true
			for keep := 1; keep <= 3; keep++ {
				if p := prnProblem(keys, e4lo, keep, wants[keep]); p != "" {
					reports++
					if reports <= prnMaxReports {
						t.Error(p)
					}
				}
			}
		})
		if reports > prnMaxReports {
			t.Errorf("... and %d more orders of keys fail the same way", reports-prnMaxReports)
		}
		if len(seen) != 720 {
			t.Errorf("test defect: prnPermute produced %d distinct orders of six keys, want 720", len(seen))
		}
	})

	// P3. current itself and every key above it are never in the result; a
	// key at current's epoch with a lower nonce is below it. Each case's
	// result is pinned exactly, not only its membership, so that a pass
	// whose window counts keys at or above current is caught (see the file's
	// comment).
	t.Run("P3", func(t *testing.T) {
		cases := []prnCase{
			{"current and every key above it are kept, keep 0", s(e5lo, e3lo, e4hi, e2lo, e3hi, e4lo), e3lo, 0, s(e2lo)},
			{"current and every key above it are kept, keep 1", s(e5lo, e3lo, e4hi, e2lo, e3hi, e4lo), e3lo, 1, nil},
			{"keys above a current not among them fill no part of the window", s(e4hi, e2lo, e3lo, e4lo, e2hi), e3hi, 1, s(e2lo, e2hi)},
			{"a lower nonce at current's epoch is below it, keep 0", s(e3hi, e4lo, e3lo, e2lo), e3hi, 0, s(e2lo, e3lo)},
			{"a lower nonce at current's epoch is the newest below it, keep 1", s(e3lo, e2hi, e3hi, e2lo), e3hi, 1, s(e2lo, e2hi)},
			{"a higher nonce at current's epoch is above it", s(e3hi, e3lo, e2hi), e3lo, 0, s(e2hi)},
			{"current is the highest key, keep 2", s(e5lo, e2lo, e4hi, e3lo), e5lo, 2, s(e2lo)},
		}
		prnRun(t, cases)
		for _, m := range prnMutants {
			caught := false
			for _, c := range cases {
				if !slices.Equal(m.fn(c.keys, c.current, c.keep), c.want) {
					caught = true
				}
			}
			if !caught {
				t.Errorf("test defect: no P3 case tells %s apart from ADR 0019 section 2's contract", m.name)
			}
		}
	})

	// P4. keep 0 returns every key below current; a negative keep acts as 0.
	t.Run("P4", func(t *testing.T) {
		mixed := s(e4lo, e2hi, e5lo, e3hi, e2lo, e3lo, e4hi)
		allBelow := s(e3hi, e2lo, e4hi, e2hi)
		prnRun(t, []prnCase{
			{"keep 0", mixed, e4lo, 0, s(e2lo, e2hi, e3lo, e3hi)},
			{"keep -1", mixed, e4lo, -1, s(e2lo, e2hi, e3lo, e3hi)},
			{"keep -2", mixed, e4lo, -2, s(e2lo, e2hi, e3lo, e3hi)},
			{"keep math.MinInt", mixed, e4lo, math.MinInt, s(e2lo, e2hi, e3lo, e3hi)},
			{"keep 0, current above every key", allBelow, e5lo, 0, s(e2lo, e2hi, e3hi, e4hi)},
			{"keep -1, current above every key", allBelow, e5lo, -1, s(e2lo, e2hi, e3hi, e4hi)},
			{"keep 0, current below every key", s(e2lo, e3lo), e1hi, 0, nil},
			{"keep -1, current below every key", s(e2lo, e3lo), e1hi, -1, nil},
			{"keep 0, one key below", s(e3lo, e2hi), e3lo, 0, s(e2hi)},
		})
	})

	// P5. keys is unchanged by the call: neither reordered nor overwritten.
	t.Run("P5", func(t *testing.T) {
		inputs := []struct {
			name    string
			keys    []string
			current string
		}{
			{"descending", s(e4hi, e4lo, e3hi, e3lo, e2hi, e2lo), e3hi},
			{"interleaved", s(e3lo, e5lo, e2hi, e4hi, e2lo), e4lo},
			{"all below, unsorted", s(e3hi, e2lo, e3lo), e5lo},
		}
		for _, in := range inputs {
			for _, keep := range []int{-1, 0, 1, 2, 10} {
				t.Run(fmt.Sprintf("%s, keep %d", in.name, keep), func(t *testing.T) {
					keys := slices.Clone(in.keys)
					_, panicked, pval := prnCall(keys, in.current, keep)
					if panicked {
						t.Errorf("prunable(%s, %s, %d) panicked with %v", prnShowAll(in.keys), prnShort(in.current),
							keep, pval)
					}
					if !slices.Equal(keys, in.keys) {
						t.Errorf("prunable(%s, %s, %d) changed keys to %s, want it unchanged (ADR 0019 section 2: "+
							"it modifies neither keys nor its elements)", prnShowAll(in.keys), prnShort(in.current),
							keep, prnShowAll(keys))
					}
				})
			}
		}
	})

	// P6. Exhaustively: for a universe of six snapshot keys over three
	// epochs with two nonces, every subset in a shuffled order, every
	// current in the universe plus one key below all of them and one above
	// all of them, and every keep from -1 to the universe's size, the result
	// equals prnOracle's, and keys is unchanged.
	t.Run("P6", func(t *testing.T) {
		universe := s(e2lo, e2hi, e3lo, e3hi, e4lo, e4hi)
		currents := append(append(s(e1hi), universe...), e5lo)
		r := rand.New(rand.NewPCG(prnSeed, ^prnSeed))
		caught := make([]bool, len(prnMutants))
		reports, checked := 0, 0
		for mask := 0; mask < 1<<len(universe); mask++ {
			var subset []string
			for i, k := range universe {
				if mask&(1<<i) != 0 {
					subset = append(subset, k)
				}
			}
			for _, current := range currents {
				keys := slices.Clone(subset)
				r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
				for keep := -1; keep <= len(universe); keep++ {
					checked++
					want := prnOracle(keys, current, keep)
					for i, m := range prnMutants {
						if !slices.Equal(m.fn(keys, current, keep), want) {
							caught[i] = true
						}
					}
					if p := prnProblem(keys, current, keep, want); p != "" {
						reports++
						if reports <= prnMaxReports {
							t.Errorf("seed %#x, subset mask %06b: %s", prnSeed, mask, p)
						}
					}
				}
			}
		}
		if reports > prnMaxReports {
			t.Errorf("... and %d more failing inputs (seed %#x)", reports-prnMaxReports, prnSeed)
		}
		if want := (1 << len(universe)) * len(currents) * (len(universe) + 2); checked != want {
			t.Errorf("test defect: P6 checked %d inputs, want %d", checked, want)
		}
		for i, m := range prnMutants {
			if !caught[i] {
				t.Errorf("test defect: no P6 input tells %s apart from ADR 0019 section 2's contract", m.name)
			}
		}
	})
}
