package core

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

// 仕様の文書の本文を読む口の、読み取りの誤りから訳の値への写し。口の振る舞いは internal/server の試験が確かめる。

func TestSpecUnreadableReasonOf(t *testing.T) { // AC-267
	for _, tc := range []struct {
		name string
		err  error
		want SpecUnreadableReason
	}{
		{"no file", &fs.PathError{Op: "stat", Path: "/ws/a.md", Err: syscall.ENOENT}, SpecUnreadableMissing},
		{"absolute", errSpecPathAbsolute, SpecUnreadableInvalidPath},
		{"outside", errSpecPathOutside, SpecUnreadableInvalidPath},
		{"unusable path", errSpecPathInvalid, SpecUnreadableInvalidPath},
		// 場所の検査の中のファイルの系の誤りは、ファイルが無い誤りを包んでいても、場所の検査の訳にする。
		{"path check wrapping not-exist", fmt.Errorf("%w: %w", errSpecPathInvalid, fs.ErrNotExist), SpecUnreadableInvalidPath},
		{"not regular", errSpecNotRegular, SpecUnreadableNotRegularFile},
		{"too large", errSpecTooLarge, SpecUnreadableTooLarge},
		{"permission", &fs.PathError{Op: "open", Path: "/ws/a.md", Err: syscall.EACCES}, SpecUnreadableOther},
		{"not a directory", &fs.PathError{Op: "stat", Path: "/ws/a.md/b", Err: syscall.ENOTDIR}, SpecUnreadableOther},
		{"read failure", &fs.PathError{Op: "read", Path: "/ws/a.md", Err: syscall.EIO}, SpecUnreadableOther},
		{"unknown", errors.New("boom"), SpecUnreadableOther},
	} {
		if got := specUnreadableReasonOf(tc.err); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
