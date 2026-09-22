package core

import "testing"

// newStoreForTest は t.TempDir() に Init 済みのワークスペースを作り、開いた
// *Store を返す。t.Cleanup で Close する。internal/core/challenge_test.go・
// internal/core/activity_test.go・internal/core/actor_test.go が共有する。
func newStoreForTest(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() setup error = %v", err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() setup error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
