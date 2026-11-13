package core

import (
	"os"
	"testing"
)

// TestMain は、テストの用意で作った Init 済みの雛形（copyInitTemplate）を、実行の後に消す。
func TestMain(m *testing.M) {
	code := m.Run()
	removeInitTemplate()
	os.Exit(code)
}
