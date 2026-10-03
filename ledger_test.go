package archiveledger

import (
	"errors"
	"reflect"
	"testing"
)

const day = secondsPerDay

// 场景一：保留天数 90 → 365 → 100。
// 旧版本仍保留 365 天，新版本只承诺 100 天。
func TestRetentionRaiseThenLower(t *testing.T) {
	l := New()

	l.SubmitPolicy(0, "vol", 90, false)
	if err := l.RegisterVersion(0, "v-old", "vol"); err != nil {
		t.Fatalf("register v-old: %v", err)
	}

	l.SubmitPolicy(10, "vol", 365, false) // 上调：v-old 承诺变为 365 天
	l.SubmitPolicy(20, "vol", 100, false) // 下调：不影响已登记版本

	if err := l.RegisterVersion(20, "v-new", "vol"); err != nil {
		t.Fatalf("register v-new: %v", err)
	}

	// 有效截止时间从原始登记时间起算，不从策略修改时刻重新计时。
	deadline, _, ok := l.QueryVersion(20, "v-old")
	if !ok || deadline != 365*day {
		t.Fatalf("v-old deadline = %d, ok = %v; want %d", deadline, ok, 365*day)
	}
	deadline, _, ok = l.QueryVersion(20, "v-new")
	if !ok || deadline != 20+100*day {
		t.Fatalf("v-new deadline = %d, ok = %v; want %d", deadline, ok, 20+100*day)
	}

	// 100 天到期时：v-new 可清除，v-old 仍在保护期。
	now := 20 + 100*day
	if _, purgeable, _ := l.QueryVersion(now, "v-new"); !purgeable {
		t.Fatal("v-new should be purgeable at its 100-day deadline")
	}
	if _, purgeable, _ := l.QueryVersion(now, "v-old"); purgeable {
		t.Fatal("v-old must still be protected by the 365-day commitment")
	}

	// 365 天到期时：v-old 可清除。
	if _, purgeable, _ := l.QueryVersion(365*day, "v-old"); !purgeable {
		t.Fatal("v-old should be purgeable at its 365-day deadline")
	}
}

// 场景二：已到期未清除版本被再次上调后不能清除；
// 冻结期间继续延期，解除冻结后按新截止时间清除。
func TestExpiredVersionExtendedAndLegalHold(t *testing.T) {
	l := New()

	l.SubmitPolicy(0, "vol", 10, false)
	if err := l.RegisterVersion(0, "v1", "vol"); err != nil {
		t.Fatalf("register v1: %v", err)
	}

	// 已到期但尚未清除。
	if _, purgeable, _ := l.QueryVersion(10*day, "v1"); !purgeable {
		t.Fatal("v1 should be purgeable at its original 10-day deadline")
	}

	// 到期未清除的版本被上调，重新进入保护期。
	l.SubmitPolicy(10*day, "vol", 40, false)
	if _, purgeable, _ := l.QueryVersion(10*day, "v1"); purgeable {
		t.Fatal("v1 must be protected again after retention raised to 40 days")
	}
	if got := l.PurgeExpired(10*day, "vol"); len(got) != 0 {
		t.Fatalf("PurgeExpired = %v; want empty", got)
	}

	// 到期时开启法律冻结：冻结阻止清除。
	l.SubmitPolicy(40*day, "vol", 40, true)
	if _, purgeable, _ := l.QueryVersion(40*day, "v1"); purgeable {
		t.Fatal("v1 must not be purgeable while legal hold is on")
	}
	if got := l.PurgeExpired(40*day, "vol"); len(got) != 0 {
		t.Fatalf("PurgeExpired under legal hold = %v; want empty", got)
	}

	// 冻结期间继续延期：承诺变为 50 天。
	l.SubmitPolicy(40*day, "vol", 50, true)

	// 解除冻结后按当时的有效截止时间（50 天）判断，45 天时仍不可清除。
	l.SubmitPolicy(45*day, "vol", 50, false)
	if _, purgeable, _ := l.QueryVersion(45*day, "v1"); purgeable {
		t.Fatal("v1 must still be protected until the new 50-day deadline")
	}
	if got := l.PurgeExpired(45*day, "vol"); len(got) != 0 {
		t.Fatalf("PurgeExpired at 45 days = %v; want empty", got)
	}

	// 到达新截止时间后可清除，清除后不再参与查询。
	if _, purgeable, _ := l.QueryVersion(50*day, "v1"); !purgeable {
		t.Fatal("v1 should be purgeable at its new 50-day deadline")
	}
	if got := l.PurgeExpired(50*day, "vol"); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("PurgeExpired = %v; want [v1]", got)
	}
	if _, _, ok := l.QueryVersion(50*day, "v1"); ok {
		t.Fatal("purged v1 must no longer be queryable")
	}
}

// 清除顺序：按有效截止时间从早到晚，同一截止时间按版本标识字典序。
func TestPurgeOrdering(t *testing.T) {
	l := New()

	l.SubmitPolicy(0, "vol", 30, false)
	// 更早到期的版本。
	if err := l.RegisterVersion(0, "early", "vol"); err != nil {
		t.Fatalf("register early: %v", err)
	}
	// 同一截止时间，标识乱序登记。
	for _, id := range []string{"b", "a", "c"} {
		if err := l.RegisterVersion(10*day, id, "vol"); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	// 更晚到期、本次不清除的版本。
	if err := l.RegisterVersion(20*day, "late", "vol"); err != nil {
		t.Fatalf("register late: %v", err)
	}

	got := l.PurgeExpired(40*day, "vol")
	want := []string{"early", "a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PurgeExpired = %v; want %v", got, want)
	}
	if _, _, ok := l.QueryVersion(40*day, "late"); !ok {
		t.Fatal("late must survive this purge")
	}
}

// 未提交策略的卷不能登记版本；已清除版本不受后续策略影响。
func TestUnknownVolumeAndPurgedImmunity(t *testing.T) {
	l := New()

	if err := l.RegisterVersion(0, "v1", "ghost"); !errors.Is(err, ErrUnknownVolume) {
		t.Fatalf("register on unknown volume: err = %v; want ErrUnknownVolume", err)
	}

	l.SubmitPolicy(0, "vol", 10, false)
	if err := l.RegisterVersion(0, "v1", "vol"); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if got := l.PurgeExpired(10*day, "vol"); !reflect.DeepEqual(got, []string{"v1"}) {
		t.Fatalf("PurgeExpired = %v; want [v1]", got)
	}

	// 清除后上调策略，已清除版本不再出现。
	l.SubmitPolicy(10*day, "vol", 365, false)
	if _, _, ok := l.QueryVersion(10*day, "v1"); ok {
		t.Fatal("purged v1 must not be affected by later policy changes")
	}
	if got := l.PurgeExpired(365*day, "vol"); len(got) != 0 {
		t.Fatalf("PurgeExpired = %v; want empty", got)
	}
}
