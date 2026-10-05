package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 取れなかった後、状態を読む前に他のプロセスが解放して idle を読んだ競合でも、
// slot_unavailable にせず取り直す。
func TestWaitForOriginalSlot_IdleAfterUnavailableRetries(t *testing.T) {
	calls := 0
	want := &SlotAssignment{}
	a, err := waitForOriginalSlot(context.Background(), time.Now().Add(time.Minute),
		func() (*SlotAssignment, error) {
			calls++
			if calls == 1 {
				return nil, ErrSlotUnavailable
			}
			return want, nil
		},
		func() (slotState, error) { return slotStateIdle, nil })
	if err != nil || a != want || calls != 2 {
		t.Fatalf("a=%v err=%v calls=%d", a, err, calls)
	}
}

func TestWaitForOriginalSlot_NeedsAttentionFails(t *testing.T) {
	_, err := waitForOriginalSlot(context.Background(), time.Now().Add(time.Minute),
		func() (*SlotAssignment, error) { return nil, ErrSlotUnavailable },
		func() (slotState, error) { return slotStateNeedsAttention, nil })
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitForOriginalSlot_ExpiredDeadlineFailsEvenIfIdle(t *testing.T) {
	_, err := waitForOriginalSlot(context.Background(), time.Now().Add(-time.Second),
		func() (*SlotAssignment, error) { return nil, ErrSlotUnavailable },
		func() (slotState, error) { return slotStateIdle, nil })
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
