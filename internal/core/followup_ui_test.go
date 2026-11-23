package core

import (
	"context"
	"testing"
)

// AC-26: 経路 ui・本人確認なしの、フォローアップを捨てる操作は成立し、記録に経路 ui が残る。
func TestFollowupDropFromTheScreen(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	f := fuCreate(t, s, "", "1", "")
	ui, err := Verify(ChannelUI, &fakeVerifier{method: VerificationNone, actor: "alice"}, "s", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DropFollowups(context.Background(), []string{f.ID}, "要らない", ui)
	if err != nil || len(got) != 1 || got[0].Status != FollowupStatusDropped {
		t.Fatalf("drop = %+v, %v", got, err)
	}
	if n := len(fuActs(t, s, "followup", ActivityActionFollowupDrop)); n != 1 {
		t.Errorf("drop activities = %d, want 1", n)
	}
}
