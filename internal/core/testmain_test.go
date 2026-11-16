package core

import (
	"os"
	"testing"
	"time"
)

// TestMain は、テストの用意で作った Init 済みの雛形（copyInitTemplate）を、実行の後に消す。
func TestMain(m *testing.M) {
	// time.Local は全テストの前に一度だけ UTC+9 にする（UTC の実行環境でも、ローカルの日付と UTC の日付を区別する）。
	// テストの途中で書き換えると、t.Parallel のテストが残した goroutine の time.Now の読み出しと競合する。
	time.Local = time.FixedZone("JST", 9*3600)
	code := m.Run()
	removeInitTemplate()
	os.Exit(code)
}
