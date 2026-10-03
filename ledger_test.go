package archiveretention

import (
	"reflect"
	"testing"
)

func TestRaiseThenLowerKeepsOldPromise(t *testing.T) {
	l := New()
	var now int64 = 1_000_000

	l.SubmitVolumePolicy(now, "vol-a", 90, false)
	l.RegisterVersion(now, "v-old", "vol-a")

	// Raise 90 -> 365: existing version's promise extends to 365.
	now += 10
	l.SubmitVolumePolicy(now, "vol-a", 365, false)

	// Lower 365 -> 100: only affects versions registered afterwards.
	now += 10
	l.SubmitVolumePolicy(now, "vol-a", 100, false)
	l.RegisterVersion(now, "v-new", "vol-a")

	oldStatus, ok := l.QueryVersion(now, "v-old")
	if !ok {
		t.Fatal("v-old should exist")
	}
	wantOld := int64(1_000_000) + 365*SecondsPerDay
	if oldStatus.EffectiveDeadline != wantOld {
		t.Fatalf("v-old deadline = %d, want %d (registered + 365d)", oldStatus.EffectiveDeadline, wantOld)
	}

	newStatus, ok := l.QueryVersion(now, "v-new")
	if !ok {
		t.Fatal("v-new should exist")
	}
	wantNew := now + 100*SecondsPerDay
	if newStatus.EffectiveDeadline != wantNew {
		t.Fatalf("v-new deadline = %d, want %d (registered + 100d)", newStatus.EffectiveDeadline, wantNew)
	}

	// At old's original 90-day deadline it must still be protected.
	at90 := int64(1_000_000) + 90*SecondsPerDay
	if s, _ := l.QueryVersion(at90, "v-old"); s.Clearable {
		t.Fatal("v-old must not be clearable at its original 90-day deadline")
	}
	// At the 365-day deadline it becomes clearable.
	if s, _ := l.QueryVersion(wantOld, "v-old"); !s.Clearable {
		t.Fatal("v-old should be clearable at its 365-day deadline")
	}
}

func TestExpiredVersionReprotectedByRaise(t *testing.T) {
	l := New()
	var now int64 = 5_000

	l.SubmitVolumePolicy(now, "vol-b", 10, false)
	l.RegisterVersion(now, "v1", "vol-b")

	// Move past the 10-day deadline: v1 is expired but not cleared.
	now += 11 * SecondsPerDay
	if s, _ := l.QueryVersion(now, "v1"); !s.Clearable {
		t.Fatal("v1 should be expired and clearable before the raise")
	}

	// Raising retention re-protects the already-expired version.
	l.SubmitVolumePolicy(now, "vol-b", 30, false)
	if s, _ := l.QueryVersion(now, "v1"); s.Clearable {
		t.Fatal("v1 must be re-protected after retention raise")
	}
	if got := l.ClearExpired(now, 10); len(got) != 0 {
		t.Fatalf("ClearExpired returned %v, want nothing while re-protected", got)
	}

	// At the new deadline (registered + 30d) it becomes clearable again.
	deadline := int64(5_000) + 30*SecondsPerDay
	if got := l.ClearExpired(deadline, 10); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("ClearExpired = %v, want [v1]", got)
	}
	if _, ok := l.QueryVersion(deadline, "v1"); ok {
		t.Fatal("cleared version must no longer be queryable")
	}
}

func TestLegalHoldBlocksClearing(t *testing.T) {
	l := New()
	var now int64 = 100

	l.SubmitVolumePolicy(now, "vol-c", 5, true)
	l.RegisterVersion(now, "v1", "vol-c")

	// Frozen: expired but not clearable, hold does not pause time.
	now += 6 * SecondsPerDay
	if s, _ := l.QueryVersion(now, "v1"); s.Clearable {
		t.Fatal("v1 must not be clearable while legal hold is on")
	}
	if got := l.ClearExpired(now, 10); len(got) != 0 {
		t.Fatalf("ClearExpired under hold = %v, want nothing", got)
	}

	// During the hold, retention is raised; promise extends.
	l.SubmitVolumePolicy(now, "vol-c", 20, true)
	wantDeadline := int64(100) + 20*SecondsPerDay
	if s, _ := l.QueryVersion(now, "v1"); s.EffectiveDeadline != wantDeadline {
		t.Fatalf("deadline under hold = %d, want %d", s.EffectiveDeadline, wantDeadline)
	}

	// Lift the hold before the new deadline: still protected by the promise.
	l.SubmitVolumePolicy(now, "vol-c", 20, false)
	if got := l.ClearExpired(now, 10); len(got) != 0 {
		t.Fatalf("ClearExpired before new deadline = %v, want nothing", got)
	}

	// At the new deadline, clearing proceeds by effective deadline.
	if got := l.ClearExpired(wantDeadline, 10); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("ClearExpired at new deadline = %v, want [v1]", got)
	}
}

func TestClearOrderingAndTieBreak(t *testing.T) {
	l := New()
	var now int64 = 0

	l.SubmitVolumePolicy(now, "vol-d", 1, false)
	l.RegisterVersion(now, "b", "vol-d")
	l.RegisterVersion(now, "a", "vol-d") // same deadline as b
	l.SubmitVolumePolicy(now, "vol-d", 2, false)
	l.RegisterVersion(now, "c", "vol-d") // later deadline

	now += 2 * SecondsPerDay
	got := l.ClearExpired(now, 2)
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("ClearExpired = %v, want [a b] (earliest deadline, ID tie-break)", got)
	}
	got = l.ClearExpired(now, 2)
	if !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("ClearExpired = %v, want [c]", got)
	}
	if got := l.ClearExpired(now, 2); len(got) != 0 {
		t.Fatalf("ClearExpired after all cleared = %v, want nothing", got)
	}
}
