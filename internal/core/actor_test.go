package core

import (
	"context"
	"errors"
	"os/user"
	"testing"
)

// withActorSource は actorSource（パッケージ変数）を src に差し替え、テスト終了時に
// t.Cleanup で元に戻す。actorSource を書き換えるテストは package core の他の
// テストと同時に走ると干渉するため t.Parallel にしない（このファイルの全テストで
// 共通の注意。#9 の設計メモに明記されている規律）。
func withActorSource(t *testing.T, src actorSourceFuncs) {
	t.Helper()
	orig := actorSource
	actorSource = src
	t.Cleanup(func() { actorSource = orig })
}

func fakeGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestResolveActor_UsesUserCurrentUsernameWhenAvailable(t *testing.T) {
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return &user.User{Username: "alice"}, nil },
		getenv:      fakeGetenv(map[string]string{"USER": "bob", "LOGNAME": "carol"}),
	})
	actor, err := resolveActor()
	if err != nil {
		t.Fatalf("resolveActor() error = %v", err)
	}
	if actor != "alice" {
		t.Fatalf("actor = %q, want %q", actor, "alice")
	}
}

func TestResolveActor_FallsBackToUSerEnvWhenUserCurrentFails(t *testing.T) {
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("boom") },
		getenv:      fakeGetenv(map[string]string{"USER": "bob", "LOGNAME": "carol"}),
	})
	actor, err := resolveActor()
	if err != nil {
		t.Fatalf("resolveActor() error = %v", err)
	}
	if actor != "bob" {
		t.Fatalf("actor = %q, want %q", actor, "bob")
	}
}

func TestResolveActor_FallsBackWhenUserCurrentUsernameIsEmpty(t *testing.T) {
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return &user.User{Username: ""}, nil },
		getenv:      fakeGetenv(map[string]string{"USER": "bob", "LOGNAME": "carol"}),
	})
	actor, err := resolveActor()
	if err != nil {
		t.Fatalf("resolveActor() error = %v", err)
	}
	if actor != "bob" {
		t.Fatalf("actor = %q, want %q", actor, "bob")
	}
}

func TestResolveActor_FallsBackToLognameEnvWhenUserEmpty(t *testing.T) {
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("boom") },
		getenv:      fakeGetenv(map[string]string{"USER": "", "LOGNAME": "carol"}),
	})
	actor, err := resolveActor()
	if err != nil {
		t.Fatalf("resolveActor() error = %v", err)
	}
	if actor != "carol" {
		t.Fatalf("actor = %q, want %q", actor, "carol")
	}
}

func TestResolveActor_ReturnsErrActorUnavailableWhenAllEmpty(t *testing.T) {
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("boom") },
		getenv:      fakeGetenv(map[string]string{}),
	})
	_, err := resolveActor()
	if !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("err = %v, want ErrActorUnavailable", err)
	}
}

// TestCreateChallenge_ActorUnavailableChangesNothing は「actor を解決できなければ
// ErrActorUnavailable で終わり、課題が作られず作業ログも増えない」ことの検証
// （§作業ログ・§クリティカル設計に準じる完了条件）。
func TestCreateChallenge_ActorUnavailableChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("boom") },
		getenv:      fakeGetenv(map[string]string{}),
	})

	before := countChallenges(t, s)
	_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "x"})
	if !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("CreateChallenge() error = %v, want ErrActorUnavailable", err)
	}
	after := countChallenges(t, s)
	if after != before {
		t.Fatalf("challenge count changed: before=%d after=%d", before, after)
	}

	activities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 0 {
		t.Fatalf("activities = %+v, want none", activities)
	}
}
