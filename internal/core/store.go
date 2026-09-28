// Package core は flywheel の core の公開 API を持つ。CLI（internal/cli）は
// この配下の公開関数だけを呼び、状態機械・承認・作業ログの規則や
// internal/core/internal/store を直接扱わない（P2。Go の internal 規則で
// store 配下は internal/core からしか import できない）。
//
// 本チケットでは、ストアを開く・ワークスペースを決める・init を行う部分だけを
// 実装する（課題・状態機械・承認は後続チケット）。
package core

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// Store はワークスペースの開いたストアへのハンドル。呼び出し側は使い終えたら
// 必ず Close する。
type Store struct {
	db        *store.DB
	workspace string

	// now はテストが時刻を差し替えるためのフック。nil なら time.Now を使う
	// （#9 §A-6）。internal/core のテストだけがこのフィールドへ直接代入する
	// （*Store は同一パッケージから生成されるため、公開セッターは持たない）。
	now func() time.Time

	// insertActivity はテストが作業ログへの書き込みを失敗させるためのフック。
	// nil なら defaultInsertActivity を使う（#9 §A-3 の失敗注入。本番 API には
	// 注入口を露出しない）。
	insertActivity func(tx *sql.Tx, row activityRow) error

	// beforeCommit はテストが「1 つの書き込みトランザクションが BEGIN IMMEDIATE の
	// 書き込みロックを保持したまま止まる」状況を作るためのフック。mutate が
	// 渡された fn を成功させた直後・コミットする前に呼ぶ。nil なら何もしない
	// （#10 AC-36 の並行テスト専用。本番 API には注入口を露出しない）。
	// テスト専用: 非テストコードから設定してはならない（設定すると書き込み
	// ロックを保持したまま止まり、他プロセスを busy_timeout まで待たせる）。
	// 本番の生成経路（openExistingAt・openAtForInit）が nil のままであることは
	// TestStore_ProductionOpenPathsLeaveBeforeCommitNil が固定する。
	beforeCommit func()

	// heartbeatInterval・staleAfter は #81（judgment.go）のテストが
	// defaultHeartbeatInterval（60秒）・defaultStaleAfter（300秒）を短く
	// 差し替えるためのフック。ゼロ値（本番）ならそれぞれの既定値を使う。
	heartbeatInterval time.Duration
	staleAfter        time.Duration

	// lockHeartbeatInterval・lockStaleAfter は #83（cycle.go）のテストが
	// defaultLockHeartbeatInterval（60秒）・defaultLockStaleAfter（300秒）を
	// 短く差し替えるためのフック。ゼロ値（本番）ならそれぞれの既定値を使う。
	// heartbeatInterval・staleAfter（run の heartbeat・stale の回収）とは別の
	// 概念（サイクルの排他ロックの heartbeat・stale の回収）のため、値は同じ
	// 既定でも独立したフィールドとして持つ。
	lockHeartbeatInterval time.Duration
	lockStaleAfter        time.Duration
}

// currentTime は now が設定されていればそれを、なければ time.Now() を返す。
func (s *Store) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Close はストアへの接続を閉じる。
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// Workspace はこのストアが開いているワークスペースの絶対パスを返す。
func (s *Store) Workspace() string { return s.workspace }

// StorePath はストアファイルの絶対パスを返す。
func (s *Store) StorePath() string { return s.db.Path() }

// wrapStoreErr は internal/store の sentinel error を core の sentinel error へ
// 写像する（internal/cli/errors.go の設計メモにある案 (a)）。ストアを開く経路
// （Init・OpenWorkspace）専用: 開く操作はアプリケーションレベルの sentinel
// （ErrNotFound 等）を返すことが無いため、既知の2値以外はすべて ErrStoreError に
// 分類してよい。
func wrapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrTooNew):
		return ErrStoreTooNew
	case errors.Is(err, store.ErrBusy):
		return ErrStoreBusy
	default:
		return fmt.Errorf("%w: %w", ErrStoreError, err)
	}
}

// classifyReadWriteErr は Store.mutate（store.DB.Write 経由）・GetChallenge・
// ListChallenges・ListActivities（store.DB.Read 経由）が返すエラーを分類する。
// これらの経路は、開く操作（wrapStoreErr が対象）と異なり、渡した関数
// （fn）自身がアプリケーションレベルの sentinel（ErrNotFound・ErrValidation・
// ErrTerminalState・ErrActorUnavailable 等）を意図して返すことがあるため、
// wrapStoreErr をそのまま使うと store_error に化けてしまう（レビュー指摘:
// store.ErrBusy が internal/core の sentinel に写像されず、書き込みロック
// 競合時の create/edit が store_busy ではなく internal_error になっていた）。
// ここでは低レベルのストアエラー（SQLITE_BUSY・スキーマ版が新しすぎる）だけを
// 写像し、それ以外（アプリケーションの sentinel を含む）はそのまま返す。
func classifyReadWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrTooNew):
		return ErrStoreTooNew
	case errors.Is(err, store.ErrBusy):
		return ErrStoreBusy
	default:
		return err
	}
}

// openExistingAt は dir/.flywheel/flywheel.db を、既に存在するときだけ開く
// （OpenWorkspace が使う）。store 層自身が「既存のみ開く」モードを強制する
// ため、呼び出し前の os.Stat が TOCTOU で外れても新規作成はされない
// （fail-closed を store 層で保証する＝完了条件）。
func openExistingAt(dir string) (*Store, error) {
	migrations, err := store.Migrations()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreError, err)
	}
	db, err := store.OpenExisting(storeDBPath(dir), migrations)
	if err != nil {
		return nil, wrapStoreErr(err)
	}
	return &Store{db: db, workspace: dir}, nil
}

// OpenWorkspace は init 以外のコマンドが使うワークスペースの決定規則で
// ストアを開く。見つからなければ ErrStoreNotFound を返し、ストアは作らない
// （fail-closed）。
//
//   - --workspace または環境変数 FLYWHEEL_WORKSPACE が明示されたときは、
//     親ディレクトリへは遡らず、その dir だけを見る（docs/features/m1-core.md
//     AC「--workspace <dir> を指定したコマンドは、カレントディレクトリに
//     関係なく <dir>/.flywheel/flywheel.db を使う」）。
//   - どちらも指定されていないときだけ、カレントディレクトリから親へ遡って
//     最初に見つかる .flywheel/flywheel.db を使う。
func OpenWorkspace(workspaceFlag string) (*Store, error) {
	base, explicit, err := resolveBaseDirExplicit(workspaceFlag)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreError, err)
	}

	if explicit {
		if !storeExistsAt(base) {
			return nil, ErrStoreNotFound
		}
		return openExistingAt(base)
	}

	dir, ok := findWorkspace(base)
	if !ok {
		return nil, ErrStoreNotFound
	}
	return openExistingAt(dir)
}

// InitResult は Init の結果。
type InitResult struct {
	Workspace string // ワークスペースの絶対パス
	StorePath string // ストアファイルの絶対パス
	Created   bool   // この呼び出しでストアを新規作成したか（false なら冪等な再実行）
}

// gitignoreContents は .flywheel/.gitignore の内容（D10: ストアを Git で追跡
// させない。付随ファイル -wal・-shm も除外する）。runs/ は #81
// （docs/features/m3-invoker-delegation.md §invoker の共通の規則「run の
// 保存先を作るとき、.flywheel/.gitignoreにrunsを除外する行が無ければ足す」）
// が足した。init の時点でこの行を含めておくことで、通常の運用（init が
// 必ず先に走る）では invoker 側の追記（internal/invoker/gitignore.go の
// ensureRunsGitignoreEntry。.gitignore が後から削除された場合の保険）に
// 頼らずに済む（self-review 指摘 round1: 2つの書き手が異なる既定内容で
// 新規作成すると、先に書いた側の内容だけが残る競合があった）。
const gitignoreContents = "# flywheel が生成する。手で編集しない。\nflywheel.db\nflywheel.db-wal\nflywheel.db-shm\nruns/\n"

// Init は `flywheel init` の本体。--workspace → 環境変数 FLYWHEEL_WORKSPACE →
// カレントディレクトリの順で対象のディレクトリを決め（親ディレクトリへの
// 遡り探索はしない＝PD6）、そこに .flywheel/flywheel.db を作る。既にあれば
// 何も変えずに成功する（冪等）。スキーマ版がこのバイナリの知る最新版より
// 新しければ、ストアのファイル・.gitignore のどちらも一切変えずに
// ErrStoreTooNew を返す。
//
// 順序は「ストアを開いて版の検査に成功した後に .gitignore を書く」（レビュー
// 指摘）: 版が新しすぎて開けない場合に .gitignore だけが書き換わる、という
// 部分適用を避ける。.gitignore は不在のときだけ作り、既存の内容（利用者が
// 追記した行を含む）には触れない（再実行は「何も変えずに成功」）。
func Init(workspaceFlag string) (*InitResult, error) {
	base, err := resolveBaseDir(workspaceFlag)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreError, err)
	}

	flywheelDir := filepath.Join(base, flywheelDirName)
	dbPath := storeDBPath(base)

	if err := os.MkdirAll(flywheelDir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrStoreError, flywheelDir, err)
	}

	s, created, err := openAtForInit(base)
	if err != nil {
		return nil, err
	}
	if err := s.Close(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreError, err)
	}

	if err := writeGitignoreIfMissing(flywheelDir); err != nil {
		return nil, err
	}

	return &InitResult{Workspace: base, StorePath: dbPath, Created: created}, nil
}

// openAtForInit は openAt と同じくストアを開く（存在しなければ作る）が、
// この呼び出しが新規作成だったか（store.DB.WasCreated）も返す。事前の
// os.Stat ではなく、開いた時点の PRAGMA user_version が 0 だったかで判定する
// （store.Open が読む値であり、呼び出し前後でファイルの状態を別途見る必要が
// ない）。
func openAtForInit(dir string) (*Store, bool, error) {
	migrations, err := store.Migrations()
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrStoreError, err)
	}
	db, err := store.Open(storeDBPath(dir), migrations)
	if err != nil {
		return nil, false, wrapStoreErr(err)
	}
	return &Store{db: db, workspace: dir}, db.WasCreated(), nil
}

// EnsureWorkspaceGitignore は workspace/.flywheel/.gitignore が無ければ、
// gitignoreContents（D10。flywheel.db・-wal・-shm・runs/を除外する）で作る。
// 既にあれば一切変更しない（writeGitignoreIfMissingと同じ規則を、workspace
// からの相対パスで呼べる形で公開する）。
//
// self-review 指摘（round3, code-reviewer PLAUSIBLE）: internal/invoker の
// ensureRunsGitignoreEntry が「.gitignoreが無ければ何もしない」に倒すと、
// §invoker の共通の規則「run の保存先を作るとき…runs/を除外する行が
// 無ければ足す」を「.gitignoreが元から無いケース」で満たせなくなる。
// invoker はこの関数を呼ぶことで、無ければ core が持つ完全な内容
// （flywheel.db等を含む）で作る（狭い内容の.gitignoreを作ってD10の保護を
// 失わせる経路を残さない）。
func EnsureWorkspaceGitignore(workspace string) error {
	return writeGitignoreIfMissing(filepath.Join(workspace, flywheelDirName))
}

func writeGitignoreIfMissing(flywheelDir string) error {
	path := filepath.Join(flywheelDir, ".gitignore")
	if _, statErr := os.Stat(path); statErr == nil {
		return nil
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("%w: stat %s: %w", ErrStoreError, path, statErr)
	}
	if err := os.WriteFile(path, []byte(gitignoreContents), 0o644); err != nil {
		return fmt.Errorf("%w: write %s: %w", ErrStoreError, path, err)
	}
	return nil
}
