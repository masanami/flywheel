package core

import (
	"context"
	"errors"
)

// --- 上流（GitHub）からの取得 IF・正規化済み Issue の型 ---
//
// docs/features/m2-github-issue-ingest.md §機能全体の設計「規則は core、
// 入出力は adapter」: core は取得の IF を定義するだけで、gh を子プロセスで
// 呼ぶ実装は internal/adapters/github（#55）が持つ。取り込みの規則
// （ポリシー・fingerprint・3分岐・close の確かめ・食い違い）を core に置く
// のは #56 の範囲であり、本チケット（#53）はここまで（IF の形と型の定義）だけを
// 置く。core のテストはこの IF の偽の実装（メモリ上の Issue の集合）で行う。

// ErrUpstreamIssueNotFound は 1 件の取得で HTTP 404 または 410 が返ったことを
// 表す（削除・移管・閲覧権限の喪失を区別しない。
// docs/features/m2-github-issue-ingest.md §上流の close の検出）。
// この sentinel を errors.Is で判定できることが、「見つからない」と「それ以外の
// 失敗」を区別するための契約である。
var ErrUpstreamIssueNotFound = errors.New("core: upstream issue not found (404 or 410)")

// ErrUpstreamIssueTransferred は 1 件の取得が、改名・移管の転送（GitHub が
// 返す 301）を辿った結果、要求と別のリポジトリの Issue を返したことを表す
// （#69。docs/features/m2-github-issue-ingest.md §上流の close の検出）。
// 削除・閲覧権限の喪失（ErrUpstreamIssueNotFound）とは別の原因であり、
// errors.Is で互いに一致しない（404 と移管を区別できることが契約）。
// confirmOpenListAbsence は ErrUpstreamIssueNotFound と同じく、この
// sentinel も upstream_state を missing に写す。
var ErrUpstreamIssueTransferred = errors.New("core: upstream issue transferred to another repository")

// UpstreamIssue は adapter が正規化して返す、上流（GitHub）の Issue 1 件
// （docs/features/m2-github-issue-ingest.md §機能全体の設計
// 「core の型（外部キー・タイトル・本文・作成者・assignee・ラベル・状態・URL）」）。
type UpstreamIssue struct {
	// ExternalKey は "<owner>/<name>#<番号>" の形（ストア全体で課題との対応を
	// 一意に識別する。§クリティカル設計決定 1「照合は external_key だけで
	// 行う」）。不変条件: ExternalKey == fmt.Sprintf("%s#%d", Repo, Number)。
	//
	// 大文字小文字の正規表記は GitHub REST API の応答（1件・一覧のどちらも
	// 応答に含まれる owner/repo の正規の表記）を採用する。宣言（sources.json）
	// の repo の表記をそのまま使わない（round2 self-review 指摘: 宣言側は
	// LoadSourcesDeclaration が既に大文字小文字を無視して重複を検出しており
	// 〈sources.go の repo 重複検査〉、Repo/ExternalKey の正規形を core 側の
	// 契約として固定しておかないと、宣言の表記のゆれをそのまま反映した
	// adapter 実装が、大文字小文字だけ違う2つの外部キーを作り、一意制約を
	// すり抜けて同じ Issue の課題が二重に作られうる。adapter（#55）はこの
	// 契約（API 応答の表記を採用する）に従うこと）。
	ExternalKey string
	// Repo は "<owner>/<name>" の形（ExternalKey から repo 部分を取り出したもの。
	// 呼び出し側が repo 単位の処理をしやすいように併せて持つ）。
	Repo string
	// Number は Issue 番号。
	Number int
	Title  string
	// Body は Issue の本文そのもの（fingerprint の入力。要約しない）。
	Body string
	// Reporter は Issue 作成者の login（正規化前の値。比較には NormalizeLogin
	// を通す）。
	Reporter string
	// Assignees は assignee の login の一覧（正規化前の値。0人・1人・複数人の
	// いずれもありうる）。
	Assignees []string
	// Labels はラベル名の一覧（urgency_labels との突き合わせは大文字小文字を
	// 区別する完全一致で行う。§冪等な作成と更新の【仮定】）。
	Labels []string
	// State は上流の状態（"open" | "closed"）。#52 が source_binding の
	// upstream_state（open|closed|missing の型）を core 側に導入した後、
	// 型名の衝突を避けつつ寄せられるならこのフィールドもその型（または
	// open・closed だけの部分集合）へ揃えることを検討する
	// （self-review 指摘。現時点では #52 の型がまだ無いため素の string とする）。
	State string
	// URL は REST API の応答の html_url（"https://github.com/<owner>/<name>/
	// issues/<番号>" の形。API のエンドポイントを指す url ではない）。
	URL string
	// Comments は観測値のコメント数（REST の `comments`。
	// docs/features/m2-github-issue-ingest.md §上流の更新の観測と既読）。
	Comments int
	// UpdatedAt は観測値の更新日時（REST の `updated_at` の文字列をそのまま
	// 持つ。加工・パースしない＝§クリティカル設計決定 1）。
	UpdatedAt string
}

// UpstreamIssueSource は取り込みの規則（core。#56）が呼ぶ、上流（GitHub）取得の
// IF。実装は internal/adapters/github（#55）が gh の子プロセス経由で行う。
// メソッド名は親要件チケット #51 の【仮定】をそのまま採用する。
// UpstreamIssueSource が返しうる失敗のうち、GetIssue の
// ErrUpstreamIssueNotFound（404・410）と ErrUpstreamIssueTransferred（移管。
// #69）以外は、この IF は個別の sentinel を
// 定義しない（「一覧の取得に失敗したリポジトリは結果に失敗として示し、他は
// 続行する」という部分成功の扱いは #56 が呼び出し結果から判断する）。
//
// 「gh が PATH に無い（upstream_unavailable。宣言全体の実行を終了コード2で
// 止める）」の判定は、UpstreamIssueSource の呼び出しの前（実装
// 〈internal/adapters/github・#55〉が UpstreamIssueSource を組み立てる時点）
// だけを契約にする（設計上の選択。ErrUpstreamIssueNotFound と同じく core が
// 専用の sentinel を置き、adapter がそれを返す形にすれば、呼び出しの後で
// gh の不在が判明した場合も core 側で errors.Is により判別できる余地は
// 原理的にはある。だが upstream_unavailable は「宣言全体を止める」判定で
// あり、gh の有無は呼び出しの前に LookPath 等で安価に確認できるため、
// その専用 sentinel を core に足すコストに見合わないと判断する）。
// 呼び出しの後で gh 起因の失敗が起きた場合、adapter はそれを（sentinel の
// 無い）通常のリポジトリ単位の失敗として core へ返す
// （round2/round3 self-review 指摘: 「呼び出し後の判別経路が無い」は
// import の向き〈adapter→core〉に起因する原理的な制約ではなく、core が
// 呼び出し後判定用の sentinel を持たないという設計上の選択の帰結である。
// 混同しないよう明記する）。
type UpstreamIssueSource interface {
	// ListOpenIssues は repo（"<owner>/<name>"）の open な Issue を全ページ返す。
	// 一覧に含まれる Pull Request（gh api の応答が pull_request キーを持つ
	// 要素）は、実装（adapter）が除いてから返す（core は Pull Request を
	// 意識しない。§機能全体の設計「adapter は core の語彙だけに依存させる」の
	// 裏返しとして、core の語彙に Pull Request は無い）。
	ListOpenIssues(ctx context.Context, repo string) ([]UpstreamIssue, error)

	// GetIssue は repo の number 番の Issue を1件取得する。見つからない
	// （HTTP 404・410）場合は errors.Is(err, ErrUpstreamIssueNotFound) が
	// true になるエラーを返す。応答が改名・移管の転送を辿って要求と別の
	// リポジトリの Issue を返した場合は、errors.Is(err, ErrUpstreamIssueTransferred)
	// が true になる別のエラーを返す（#69）。それ以外の失敗はどちらの
	// sentinel にも一致しないエラーを返す（呼び出し側はこの3つを区別する）。
	GetIssue(ctx context.Context, repo string, number int) (UpstreamIssue, error)

	// CurrentLogin は認証しているアカウントの login を返す（self_assignees を
	// 省略した取り込み元の解決に使う。§取り込みの対象）。
	CurrentLogin(ctx context.Context) (string, error)
}

// UpstreamPullRequest は上流（GitHub）の Pull Request 1 件（委譲の後の照合が、
// ブランチを head とする PR の URL・状態・base を run の成果物へ記録するための
// 正規化済みの値。docs/features/m3-invoker-delegation.md §合流と照合）。
type UpstreamPullRequest struct {
	// URL は PR の html_url。
	URL string
	// Title は PR のタイトル（release の要約に使う）。
	Title string
	// State は "open" | "closed" | "merged"（マージ済みは merged_at を持つ PR）。
	State string
	// Base は PR の base のブランチ名。
	Base string
}

// UpstreamBranchSource は委譲の後の照合（core）が呼ぶ、リモートのブランチと
// head ブランチの PR の取得 IF。実装は internal/adapters/github が `gh api` の
// GET だけで行う。照合は子の報告に依らず、この IF が返した値だけを記録する。
type UpstreamBranchSource interface {
	// BranchExists は repo（"<owner>/<name>"）のリモートに branch があるかを返す
	// （無いことはエラーではなく false）。
	BranchExists(ctx context.Context, repo, branch string) (bool, error)

	// ListPullRequestsByHead は repo の、branch を head とする PR を open・closed・
	// merged のすべてについて返す。
	ListPullRequestsByHead(ctx context.Context, repo, branch string) ([]UpstreamPullRequest, error)
}
