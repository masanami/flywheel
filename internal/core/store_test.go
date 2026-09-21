package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

func insertChallenge(t *testing.T, s *Store, title string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			title, "unclassified", "alice", "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
		)
		return err
	}); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}
}

func countChallenges(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM challenge").Scan(&count)
	}); err != nil {
		t.Fatalf("count challenges: %v", err)
	}
	return count
}

func TestInit_CreatesStoreAtResolvedWorkspace(t *testing.T) {
	dir := t.TempDir()

	res, err := Init(dir)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if !res.Created {
		t.Fatal("Created = false, want true (最初の init)")
	}
	if _, err := os.Stat(res.StorePath); err != nil {
		t.Fatalf("store file not created: %v", err)
	}
	if res.StorePath != storeDBPath(dir) {
		t.Fatalf("StorePath = %q, want %q", res.StorePath, storeDBPath(dir))
	}
}

func TestInit_ReRunIsIdempotentAndPreservesExistingData(t *testing.T) {
	dir := t.TempDir()

	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() 1回目 error = %v", err)
	}

	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	insertChallenge(t, s, "t")
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	res, err := Init(dir)
	if err != nil {
		t.Fatalf("Init() 2回目 error = %v", err)
	}
	if res.Created {
		t.Fatal("Created = true on the second (idempotent) run, want false")
	}

	s2, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() 検証 error = %v", err)
	}
	defer func() { _ = s2.Close() }()
	if count := countChallenges(t, s2); count != 1 {
		t.Fatalf("challenge count = %d, want 1 (再実行で既存の課題が残ること)", count)
	}
}

func TestInit_WritesGitignoreExcludingStoreAndItsSidecarFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	gitignorePath := filepath.Join(dir, flywheelDirName, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{"flywheel.db", "flywheel.db-wal", "flywheel.db-shm"} {
		if !strings.Contains(string(content), want) {
			t.Errorf(".gitignore does not exclude %q:\n%s", want, content)
		}
	}
}

// TestInit_GitStatusPorcelainDoesNotShowTheStoreOrItsSidecarFiles は
// AC「init の後、ワークスペースの Git で git status --porcelain を実行しても、
// flywheel.db・-wal・-shm が現れない」の検証。
//
// --untracked-files=all を必ず付ける: デフォルト（normal）モードは、ディレクトリ
// 全体が未追跡なら個々のファイル名を出さず "?? .flywheel/" のように 1 行へ畳む。
// このワークスペースは他に何もコミットしていないため、.gitignore がまったく
// 効いていなくても .flywheel/ 自体が丸ごと未追跡でこの畳み込みが起こり、
// 以下の strings.Contains は常に真になってしまう（検出力ゼロのテストになる。
// レビュー指摘）。--untracked-files=all で個々のファイル名を展開させて初めて、
// 除外が効いているかどうかを実際に検証できる。
func TestInit_GitStatusPorcelainDoesNotShowTheStoreOrItsSidecarFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")

	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// WAL・SHM の付随ファイルが実際に存在する状態で検証する（無ければ
	// 「除外されているから見えない」のか「そもそも無いから見えない」のか
	// 区別できない）。
	touch(t, filepath.Join(dir, flywheelDirName, "flywheel.db-wal"))
	touch(t, filepath.Join(dir, flywheelDirName, "flywheel.db-shm"))

	out := runGit(t, dir, "status", "--porcelain", "--untracked-files=all")
	for _, unwanted := range []string{"flywheel.db", "flywheel.db-wal", "flywheel.db-shm"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("git status --porcelain --untracked-files=all unexpectedly shows %q:\n%s", unwanted, out)
		}
	}

	// この検査に本当に検出力があること（.gitignore を退避して壊せば落ちること）
	// を確認する。
	gitignorePath := filepath.Join(dir, flywheelDirName, ".gitignore")
	backup := gitignorePath + ".bak"
	if err := os.Rename(gitignorePath, backup); err != nil {
		t.Fatalf("rename .gitignore aside: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(backup, gitignorePath) })

	outWithoutGitignore := runGit(t, dir, "status", "--porcelain", "--untracked-files=all")
	if !strings.Contains(outWithoutGitignore, "flywheel.db") {
		t.Fatalf("expected git status to show flywheel.db once .gitignore is removed (detection-power check); got:\n%s", outWithoutGitignore)
	}
}

func TestInit_DoesNotSearchAncestorsAndDoesNotAffectParentStore(t *testing.T) {
	parent := t.TempDir()
	if _, err := Init(parent); err != nil {
		t.Fatalf("Init(parent) error = %v", err)
	}

	child := filepath.Join(parent, "child")
	mustMkdirAll(t, child)
	res, err := Init(child)
	if err != nil {
		t.Fatalf("Init(child) error = %v", err)
	}
	if res.Workspace != child {
		t.Fatalf("Init(child) created a store at %q, want %q (親へ遡らない＝PD6)", res.Workspace, child)
	}

	// 親のストアが変わっていないこと（サイズや存在を軽く確認）。
	if _, err := os.Stat(storeDBPath(parent)); err != nil {
		t.Fatalf("parent store missing after Init(child): %v", err)
	}
}

func TestOpenWorkspace_NotFoundWhenNoStoreExists(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenWorkspace(dir)
	if !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("OpenWorkspace() error = %v, want ErrStoreNotFound", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, flywheelDirName)); statErr == nil {
		t.Fatal("OpenWorkspace() created a store directory despite not finding one (fail-closed 違反)")
	}
}

// TestOpenWorkspace_UsesAncestorStoreFromSubdirectory は、--workspace も
// FLYWHEEL_WORKSPACE も指定されていない（カレントディレクトリが基点の）ときに
// 限って、親ディレクトリへ遡ってストアを見つけることを検証する。--workspace が
// 明示されたときは遡らないこと（item 6）は
// TestOpenWorkspace_ExplicitWorkspaceFlagDoesNotAscendToAnAncestorStore が
// 別途検証する。
func TestOpenWorkspace_UsesAncestorStoreFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	child := filepath.Join(root, "a", "b")
	mustMkdirAll(t, child)
	t.Setenv(WorkspaceEnvVar, "") // cwd 起点の探索を使うため、環境を中和する。
	t.Chdir(child)

	s, err := OpenWorkspace("")
	if err != nil {
		t.Fatalf("OpenWorkspace(\"\") error = %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.Workspace() != root {
		t.Fatalf("OpenWorkspace(\"\").Workspace() = %q, want %q", s.Workspace(), root)
	}
}

// TestOpenWorkspace_ExplicitWorkspaceFlagDoesNotAscendToAnAncestorStore は
// レビュー指摘（item 6）の検証: --workspace で指す子ディレクトリにストアが無い
// 場合、親ディレクトリにストアがあっても遡らず store_not_found になる
// （docs/features/m1-core.md AC「--workspace <dir> を指定したコマンドは、
// カレントディレクトリに関係なく <dir>/.flywheel/flywheel.db を使う」）。
// 遡りはカレントディレクトリを基点にしたときだけ（TestOpenWorkspace_
// UsesAncestorStoreFromSubdirectory と対で見る）。
func TestOpenWorkspace_ExplicitWorkspaceFlagDoesNotAscendToAnAncestorStore(t *testing.T) {
	parent := t.TempDir()
	if _, err := Init(parent); err != nil {
		t.Fatalf("Init(parent) error = %v", err)
	}
	child := filepath.Join(parent, "child")
	mustMkdirAll(t, child)

	_, err := OpenWorkspace(child)
	if !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("OpenWorkspace(--workspace=child) error = %v, want ErrStoreNotFound (親へ遡ってはならない)", err)
	}
}

func TestOpenWorkspace_TooNewRejectsWithoutChangingTheFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	setFutureVersion(t, storeDBPath(dir))

	before, err := os.ReadFile(storeDBPath(dir))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	_, err = OpenWorkspace(dir)
	if !errors.Is(err, ErrStoreTooNew) {
		t.Fatalf("OpenWorkspace() error = %v, want ErrStoreTooNew", err)
	}

	after, err := os.ReadFile(storeDBPath(dir))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("store_too_new must not change the store file")
	}
}

func TestInit_TooNewRejectsWithoutChangingTheFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	setFutureVersion(t, storeDBPath(dir))

	before, err := os.ReadFile(storeDBPath(dir))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	_, err = Init(dir)
	if !errors.Is(err, ErrStoreTooNew) {
		t.Fatalf("Init() error = %v, want ErrStoreTooNew", err)
	}

	after, err := os.ReadFile(storeDBPath(dir))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("store_too_new must not change the store file (init included)")
	}
}

// TestInit_TooNewDoesNotRewriteGitignore は item 5(d) の検証: スキーマ版が
// 新しすぎて開けない init は、.gitignore にも一切触れない（ストアを開いて版の
// 検査に成功した後にだけ .gitignore を書く、という順序の検証）。
func TestInit_TooNewDoesNotRewriteGitignore(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	gitignorePath := filepath.Join(dir, flywheelDirName, ".gitignore")
	before, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore before: %v", err)
	}

	setFutureVersion(t, storeDBPath(dir))

	if _, err := Init(dir); !errors.Is(err, ErrStoreTooNew) {
		t.Fatalf("Init() error = %v, want ErrStoreTooNew", err)
	}

	after, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf(".gitignore changed after a too-new init:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestInit_ReRunPreservesUserAppendedGitignoreLines は item 5(b)(d) の検証:
// .gitignore は不在のときだけ作る。利用者が既存の .gitignore に行を追記した後で
// 再実行しても、その行は消えない（再実行は「何も変えずに成功」）。
func TestInit_ReRunPreservesUserAppendedGitignoreLines(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() 1回目 error = %v", err)
	}

	gitignorePath := filepath.Join(dir, flywheelDirName, ".gitignore")
	original, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	appended := string(original) + "my-custom-ignore-line\n"
	if err := os.WriteFile(gitignorePath, []byte(appended), 0o644); err != nil {
		t.Fatalf("append to .gitignore: %v", err)
	}

	res, err := Init(dir)
	if err != nil {
		t.Fatalf("Init() 2回目 error = %v", err)
	}
	if res.Created {
		t.Fatal("Created = true on a re-run, want false (idempotent)")
	}

	got, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore after re-init: %v", err)
	}
	if !strings.Contains(string(got), "my-custom-ignore-line") {
		t.Fatalf("re-init dropped the user-appended line, got:\n%s", got)
	}
}

// --- helpers ---

func setFutureVersion(t *testing.T, dbPath string) {
	t.Helper()
	migrations, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations(): %v", err)
	}
	db, err := store.Open(dbPath, migrations)
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("PRAGMA user_version = 999999")
		return err
	}); err != nil {
		t.Fatalf("set future user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}
