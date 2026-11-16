package core

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

// combinedOutputRetrySignal は、子プロセスがシグナルで終わったときだけ最大 3 回まで作り直し、
// 終了コードでの失敗はやり直さない。
func TestCombinedOutputRetrySignal(t *testing.T) {
	t.Parallel()
	signaled := func() *exec.Cmd { return exec.Command("sh", "-c", "kill -KILL $$") }
	cases := []struct {
		name      string
		seq       []func() *exec.Cmd
		wantCalls int
		wantOK    bool
		wantSig   bool
	}{
		{"成功はそのまま返す", []func() *exec.Cmd{func() *exec.Cmd { return exec.Command("sh", "-c", "echo ok") }}, 1, true, false},
		{"終了コードの失敗はやり直さない", []func() *exec.Cmd{func() *exec.Cmd { return exec.Command("sh", "-c", "exit 3") }}, 1, false, false},
		{"シグナルの後に成功すれば成功を返す", []func() *exec.Cmd{signaled, func() *exec.Cmd { return exec.Command("sh", "-c", "echo ok") }}, 2, true, false},
		{"シグナルの後の終了コードの失敗ではやり直しをやめる", []func() *exec.Cmd{signaled, func() *exec.Cmd { return exec.Command("sh", "-c", "exit 3") }, signaled}, 2, false, false},
		{"シグナルが続けば 3 回で諦める", []func() *exec.Cmd{signaled, signaled, signaled, signaled}, 3, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			_, err := combinedOutputRetrySignal(func() *exec.Cmd {
				if calls >= len(c.seq) {
					t.Fatalf("想定より多く呼ばれた（%d 回目）", calls+1)
				}
				cmd := c.seq[calls]
				calls++
				return cmd()
			})
			if calls != c.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, c.wantCalls)
			}
			if (err == nil) != c.wantOK {
				t.Fatalf("err = %v, wantOK %v", err, c.wantOK)
			}
			var ee *exec.ExitError
			sig := errors.As(err, &ee) && ee.Sys().(syscall.WaitStatus).Signaled()
			if sig != c.wantSig {
				t.Fatalf("signaled = %v, want %v (err %v)", sig, c.wantSig, err)
			}
		})
	}
}
