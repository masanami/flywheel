package core

import (
	"context"
	"database/sql"
	"runtime/debug"
	"testing"
	"time"
)

func TestVersionFromSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []debug.BuildSetting
		want string
	}{
		{"no vcs", nil, ""},
		{"clean", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "false"}}, "abc"},
		{"dirty", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}, "abc+dirty"},
		{"modified without revision", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
	} {
		if got := versionFromSettings(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// go test のバイナリには VCS の情報が無く NULL になる（M45P22）。
func TestBuildFlywheelVersion_NullUnderGoTest(t *testing.T) {
	if got := buildFlywheelVersion(); got != "" {
		t.Skipf("VCS info present: %q", got)
	}
}

func setFlywheelVersionForTest(t *testing.T, v string) {
	t.Helper()
	old := flywheelVersion
	flywheelVersion = func() string { return v }
	t.Cleanup(func() { flywheelVersion = old })
}

// AC-306/307: すべての run の行（judgment・delegate・predict）に版が書かれ、runs と show に出る。
func TestInsertRun_WritesFlywheelVersionForEveryKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		want    *string
	}{
		{"fixed", "deadbeef+dirty", strPtr("deadbeef+dirty")},
		{"null", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFlywheelVersionForTest(t, tc.version)
			s := newStoreForTest(t)
			ctx := context.Background()
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			ch := cardChallenge(t, s, StatusInProgress)
			cid, _ := parseChallengeID(ch.ID)
			ch2 := cardChallenge(t, s, StatusInProgress)
			cid2, _ := parseChallengeID(ch2.ID)
			base := insertRunInput{ChallengeVersion: 1, SessionID: "55555555-5555-4555-8555-555555555555", PID: 2147483646, Host: cardHost(t),
				HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketImpl}
			j := base
			j.Kind, j.Judgment = runKindJudgment, judgmentJ5
			d := base
			d.Kind = runKindDelegate
			p := insertRunInput{Kind: runKindPredict, Repo: "a/b", PID: 2147483646, Host: cardHost(t), HeartbeatAt: now, StartedAt: now,
				MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketPredict}
			err := s.db.Write(ctx, func(tx *sql.Tx) error {
				if _, err := insertRun(ctx, tx, cid, j); err != nil {
					return err
				}
				if _, err := insertRun(ctx, tx, cid2, d); err != nil {
					return err
				}
				_, err := insertRun(ctx, tx, 0, p)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			runs, err := s.ListRunsWithoutReap(ctx, RunListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 3 {
				t.Fatalf("runs = %d", len(runs))
			}
			for _, r := range runs {
				if (r.FlywheelVersion == nil) != (tc.want == nil) || (tc.want != nil && *r.FlywheelVersion != *tc.want) {
					t.Errorf("%s %s: FlywheelVersion = %v, want %v", r.ID, r.Kind, r.FlywheelVersion, tc.want)
				}
			}
			detail, err := s.GetChallengeWithoutReap(ctx, ch.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Runs) != 1 || (detail.Runs[0].FlywheelVersion == nil) != (tc.want == nil) {
				t.Errorf("show runs = %+v", detail.Runs)
			}
		})
	}
}

// show の challenge.followup_of（NULL なら nil）。
func TestShowChallenge_FollowupOf(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	a := cardChallenge(t, s, StatusInProgress)
	d, err := s.GetChallengeWithoutReap(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.FollowupOf != nil {
		t.Errorf("FollowupOf = %v, want nil", *d.FollowupOf)
	}
	n, _ := parseChallengeID(a.ID)
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET origin = 'followup', followup_of = 'C-99' WHERE id = ?`, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err = s.GetChallengeWithoutReap(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.FollowupOf == nil || *d.FollowupOf != "C-99" {
		t.Errorf("FollowupOf = %v", d.FollowupOf)
	}
}
