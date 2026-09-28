package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// --- 上流の本文・全コメントと参照先の Issue の取得（#80） ---
//
// docs/features/m3-invoker-delegation.md §J2 計画・決定 M3P14: J2 の起動の
// 直前に、取り込み元の対応のある課題の上流 Issue の本文と全コメントを取得し、
// 本文・コメントに現れる Issue への参照（3 つの書き方）を出現順に最大 5 件
// （深さ 1）取得する。J2 の標準入力への組み込み・200,000 バイトの切り詰め・
// 取得失敗時の J2 の扱いは #85 の範囲であり、本ファイルは「取得して
// UpstreamContext を組み立てる」ところまでを持つ。
//
// UpstreamIssueSource（#55）は変更しない。ingest（#56〜#58）の偽の実装を
// 壊さないため、上流のスレッド・参照先の取得は別の IF（UpstreamThreadSource）
// として追加する。

// maxUpstreamReferences は 1 課題あたり取得する参照先 Issue の上限
// （AC-89。出現順の最初の 5 件だけを取得する）。
const maxUpstreamReferences = 5

// UpstreamComment は上流 Issue の 1 件のコメント（docs/features/
// m3-invoker-delegation.md §J2 計画「本文と全コメントを取得」）。
type UpstreamComment struct {
	// Author はコメント作成者の login（正規化前の値。UpstreamIssue.Reporter と
	// 同じ扱い）。
	Author string
	// Body はコメント本文そのもの（要約しない）。
	Body string
	// CreatedAt は上流の作成日時の文字列をそのまま持つ（加工・パースしない。
	// UpstreamIssue.UpdatedAt と同じ方針）。
	CreatedAt string
	// URL はコメントの html_url。
	URL string
}

// UpstreamIssueThread は上流 Issue 1 件の本文と全コメント（API の順＝作成順）。
type UpstreamIssueThread struct {
	Issue    UpstreamIssue
	Comments []UpstreamComment
}

// UpstreamReference は本文・コメントから抽出した Issue への参照 1 件
// （ExtractUpstreamReferences の出力。同じリポジトリの `#<番号>` は
// selfRepo に解決済みの Repo を持つ）。
type UpstreamReference struct {
	Repo   string
	Number int
}

// UpstreamReferencedIssue は 1 件の UpstreamReference を取得しようとした結果。
// Fetched が false のとき、その参照は 404・410・移管のいずれかで取得できな
// かったことを表し（打ち間違いの参照で全体を止めないための「取得できな
// かった参照の記録」。Issue はゼロ値のまま）、それ以外の失敗は
// FetchUpstreamContext 全体の失敗として返す（この構造体には現れない）。
//
// round2 self-review 指摘: 参照先が Pull Request（応答が pull_request キーを
// 持つ）だった場合も、GetReferencedIssue の実装（internal/adapters/github）
// は ErrUpstreamIssueNotFound を返し、この構造体では 404・410 と同じ
// Fetched=false になる（core の語彙に PR が無い＝§機能全体の設計。URL 形式の
// 参照が "/pull/" を明示的に対象外にしているのと、書き方によって扱いが
// 食い違わないようにする）。「取得できなかった理由」を区別する情報はこの
// 構造体には持たせていない（【仮定】。#85 で理由を J2 の入力に書く必要が
// 出た場合は、Fetched bool を理由の列挙に置き換えることを検討する）。
type UpstreamReferencedIssue struct {
	Reference UpstreamReference
	Issue     UpstreamIssue
	Fetched   bool
}

// UpstreamContext は J2・J3 が読む上流の入力全体: 課題自身のスレッド（本文＋
// 全コメント）と、参照先 Issue の取得結果の一覧（出現順・最大 maxUpstreamReferences
// 件・深さ 1）。
type UpstreamContext struct {
	Thread     UpstreamIssueThread
	References []UpstreamReferencedIssue
}

// UpstreamThreadSource は上流 Issue のスレッド全体と、参照先 Issue の本文を
// 取得する IF。UpstreamIssueSource（#55）とは別の IF にする（ingest の偽の
// 実装を変えないため）。実装は internal/adapters/github が持つ。
type UpstreamThreadSource interface {
	// GetIssueThread は repo の number 番の Issue の本文と全コメントを返す。
	// 失敗の sentinel の契約は UpstreamIssueSource.GetIssue と同じ
	// （ErrUpstreamIssueNotFound・ErrUpstreamIssueTransferred・それ以外）。
	//
	// 契約（#85 の申し送り。self-review 指摘）: 返り値の
	// UpstreamIssueThread.Issue に含まれる観測値（Comments の件数・UpdatedAt）
	// は、Comments（コメント一覧）の取得より前に得た値であること。#85 は
	// M3P14・QH14（「取得と記録の間に取り込まれたコメントを読まずに既読に
	// する経路を塞ぐ」）に従い、J2 の run が成功した時点でこの観測値を使って
	// 上流を読んだ記録を付ける。実装（internal/adapters/github）が Issue 本体
	// を先に取得し、その後でコメントをページ送りする順序を守らないと、この
	// 前提が崩れる。
	GetIssueThread(ctx context.Context, repo string, number int) (UpstreamIssueThread, error)

	// GetReferencedIssue は参照先 Issue の本文だけを返す（コメントは取得しない
	// ＝AC-90「参照先のコメントも取得しない」）。失敗の sentinel の契約は
	// GetIssueThread と同じ。加えて、応答が Pull Request を指していた場合も
	// ErrUpstreamIssueNotFound を返すこと（round2 self-review 指摘。
	// UpstreamReferencedIssue のコメント参照）。
	GetReferencedIssue(ctx context.Context, repo string, number int) (UpstreamIssue, error)
}

// upstreamRefURLRe は "https://github.com/<owner>/<name>/issues/<番号>" の形の
// 参照（"/pull/" は対象外。後ろに "#issuecomment-…" 等が続いても、その部分は
// この regexp が食わないので問題にならない）。
var upstreamRefURLRe = regexp.MustCompile(`https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)/issues/([0-9]+)`)

// upstreamRefOwnerNameRe は "<owner>/<name>#<番号>" の形の参照。
var upstreamRefOwnerNameRe = regexp.MustCompile(`([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)#([0-9]+)`)

// upstreamRefBareRe は "#<番号>" の形の参照（同じリポジトリ）。境界の判定
// （直前が英数字・"/"・"_"・"." 以外、直後が英数字・"_" でない）は正規表現の
// 外で isRefBoundaryOK が行う（Go の regexp は先読み・後読みを持たないため）。
var upstreamRefBareRe = regexp.MustCompile(`#([0-9]+)`)

// extractedRef は 1 テキスト（本文または 1 コメントの本文）の中で見つかった
// 参照の内部表現（開始バイト位置つき。テキスト内の出現順に並べるために使う）。
type extractedRef struct {
	start  int
	repo   string
	number int
}

// isASCIIAlnum は r が ASCII の英数字であるかを判定する。self-review
// 指摘: 当初 unicode.IsLetter・unicode.IsDigit を使っていたが、これは
// 仮名・漢字も「英数字」として弾いてしまい、日本語の本文で「#12を参照」の
// ような（区切りの空白を挟まない）bare 参照が抽出できなかった
// （AC-88 は書き方を問わず扱うことを求めており、日本語の Issue 本文が普通
// なこのリポジトリでは looking-at-Unicode-letters は誤り）。境界の判定は
// 「<owner>/<name>#<番号>」の構文自体が ASCII 前提（GitHub の repo 名は
// ASCII の英数字・"-"・"_"・"."）であることに合わせ、ASCII だけを見る。
func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// isWordish は r が「英数字・"/"・"_"・"."・"&"」のいずれかであるかを判定する
// （isRefBoundaryOK の直前境界の判定に使う）。"&" は HTML の数値文字参照
// （"&#39;"）を参照として拾わないため（番号 0 は抽出の側で除く）。
func isWordish(r rune) bool {
	// round2 self-review 指摘（PLAUSIBLE・低優先度だが低コストなので対応）:
	// "." を含めないと、"gitlab.com/group#12" のような他ホストのドメインの
	// 一部が owner/name#N として誤って拾われる（"." の直前の "com/group#12"
	// を境界判定が通してしまう）。"." を word-ish に含めることで、ドット区切り
	// の識別子（ドメイン名・バージョン文字列等）に隣接する誤検出を防ぐ。
	return isASCIIAlnum(r) || r == '/' || r == '_' || r == '.' || r == '&'
}

// isAlnumOrUnderscore は r が「英数字・"_"」のいずれかであるかを判定する
// （isRefBoundaryOK の直後境界の判定に使う）。
func isAlnumOrUnderscore(r rune) bool {
	return isASCIIAlnum(r) || r == '_'
}

// isRefBoundaryOK は text の [start,end) にある一致が、単独の参照として
// 扱ってよい境界を持つかどうかを判定する（"abc#1"・"#12abc" を拾わない。
// "# 見出し" は upstreamRefBareRe 自体が数字を要求するため一致しない）。
//
// self-review 指摘（構造問題）: 当初は bare "#<番号>" にしか適用していな
// かったが、"<owner>/<name>#<番号>" にも左側の境界が無いと、パスや他ホストの
// URL の一部（例: "docs/a/b#2"・"https://gitlab.com/foo/bar#12"）を repo
// への参照として誤って拾ってしまう（誤って拾った参照は 404 でスキップに
// なるが、それでも maxUpstreamReferences の枠を無駄に消費する）。3 つの
// 書き方すべてに同じ境界判定を適用する（URL 形式は「https://」の直前・
// 番号の直後がこの境界を満たすことが普通であり、既存の「URL の末尾に
// "#issuecomment-…" 等が続いてもよい」という仮定と矛盾しない＝その場合
// 番号の直後は "#" であり isAlnumOrUnderscore ではないため境界判定を通る）。
func isRefBoundaryOK(text string, start, end int) bool {
	if start > 0 {
		prev, _ := utf8.DecodeLastRuneInString(text[:start])
		if prev != utf8.RuneError && isWordish(prev) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if next != utf8.RuneError && isAlnumOrUnderscore(next) {
			return false
		}
	}
	return true
}

// markConsumed は [start,end) の各バイト位置を consumed へ記録する。
func markConsumed(consumed []bool, start, end int) {
	for i := start; i < end && i < len(consumed); i++ {
		consumed[i] = true
	}
}

// overlapsConsumed は [start,end) の範囲が consumed に 1 バイトでも重なるかを
// 判定する。
func overlapsConsumed(consumed []bool, start, end int) bool {
	for i := start; i < end && i < len(consumed); i++ {
		if consumed[i] {
			return true
		}
	}
	return false
}

// extractReferencesFromText は 1 つのテキスト（本文か、1 件のコメントの本文）
// から、出現順に参照を抽出する。selfRepo は同じリポジトリの "#<番号>" を
// 解決するための課題自身のリポジトリ。
//
// URL 形式・owner/name#N 形式を先に見つけて範囲を consumed に記録し、
// bare #N 形式はその範囲と重ならないものだけを採用する（URL・owner/name#N の
// 中の部分が bare #N として二重に拾われるのを防ぐ。実際には owner/name#N の
// "#" の直前は英数字であり isRefBoundaryOK が単独でも排除するが、
// consumed による排除はその設計意図を明示する二重の防御）。
func extractReferencesFromText(selfRepo, text string) []extractedRef {
	consumed := make([]bool, len(text))
	var out []extractedRef

	for _, m := range upstreamRefURLRe.FindAllStringSubmatchIndex(text, -1) {
		if !isRefBoundaryOK(text, m[0], m[1]) {
			continue
		}
		markConsumed(consumed, m[0], m[1])
		number, err := strconv.Atoi(text[m[6]:m[7]])
		if err != nil || number <= 0 {
			continue
		}
		out = append(out, extractedRef{start: m[0], repo: text[m[2]:m[3]] + "/" + text[m[4]:m[5]], number: number})
	}

	for _, m := range upstreamRefOwnerNameRe.FindAllStringSubmatchIndex(text, -1) {
		if overlapsConsumed(consumed, m[0], m[1]) {
			continue
		}
		if !isRefBoundaryOK(text, m[0], m[1]) {
			continue
		}
		markConsumed(consumed, m[0], m[1])
		number, err := strconv.Atoi(text[m[6]:m[7]])
		if err != nil || number <= 0 {
			continue
		}
		out = append(out, extractedRef{start: m[0], repo: text[m[2]:m[3]] + "/" + text[m[4]:m[5]], number: number})
	}

	for _, m := range upstreamRefBareRe.FindAllStringSubmatchIndex(text, -1) {
		if overlapsConsumed(consumed, m[0], m[1]) {
			continue
		}
		if !isRefBoundaryOK(text, m[0], m[1]) {
			continue
		}
		number, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || number <= 0 {
			continue
		}
		out = append(out, extractedRef{start: m[0], repo: selfRepo, number: number})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// ExtractUpstreamReferences は課題自身の本文とコメント（この順・コメントは
// 引数の順＝作成順）から、Issue への参照を出現順に重複無く抽出する
// （AC-88）。3 つの書き方（同じリポジトリの "#<番号>"・"<owner>/<name>#<番号>"・
// "https://github.com/<owner>/<name>/issues/<番号>"）を扱う。
//
// 重複は大文字小文字を無視した "<owner>/<name>#<番号>" で 1 件に数え、最初の
// 出現を残す。課題自身への参照（selfRepo・selfNumber と一致するもの。大文字
// 小文字は無視）は除く。
//
// Markdown のコードブロックは解釈しない（【仮定】。コードブロック内の参照らしい
// 文字列も抽出対象になる）。件数の上限（maxUpstreamReferences）はここでは
// 適用しない（呼び出し側の FetchUpstreamContext が適用する）。
func ExtractUpstreamReferences(selfRepo string, selfNumber int, body string, comments []UpstreamComment) []UpstreamReference {
	var all []extractedRef
	all = append(all, extractReferencesFromText(selfRepo, body)...)
	for _, c := range comments {
		all = append(all, extractReferencesFromText(selfRepo, c.Body)...)
	}

	seen := make(map[string]bool, len(all))
	out := make([]UpstreamReference, 0, len(all))
	for _, r := range all {
		if strings.EqualFold(r.repo, selfRepo) && r.number == selfNumber {
			continue
		}
		key := strings.ToLower(r.repo) + "#" + strconv.Itoa(r.number)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, UpstreamReference{Repo: r.repo, Number: r.number})
	}
	return out
}

// FetchUpstreamContext は課題自身の上流スレッドを取得し、そこから抽出した
// 参照先 Issue を出現順の最初 maxUpstreamReferences 件だけ取得して
// UpstreamContext を組み立てる（AC-87・AC-89・AC-90。決定 M3P14）。
//
// 課題自身のスレッドの取得失敗は全体の失敗として返す。参照先の取得が
// ErrUpstreamIssueNotFound・ErrUpstreamIssueTransferred で失敗した場合は
// その参照を「取得できなかった」として記録してスキップし（打ち間違いの
// 参照で全体を止めない）、それ以外の失敗は全体の失敗として返す。
//
// スキップした参照も maxUpstreamReferences の枠を消費する（【仮定】。
// 出現順の最初の 5 件＝取得を試みる対象であり、6 件目でその枠を埋め合わせ
// ない。5 件のうち何件が実際に取得できたかは呼び出し側が
// UpstreamReferencedIssue.Fetched で判別する）。
// 参照先の Issue の本文からは抽出しない（深さ 1）。参照先のコメントも
// 取得しない（GetReferencedIssue の契約）。
func FetchUpstreamContext(ctx context.Context, src UpstreamThreadSource, repo string, number int) (UpstreamContext, error) {
	thread, err := src.GetIssueThread(ctx, repo, number)
	if err != nil {
		return UpstreamContext{}, fmt.Errorf("core: fetch upstream context %s#%d: %w", repo, number, err)
	}

	refs := ExtractUpstreamReferences(repo, number, thread.Issue.Body, thread.Comments)
	if len(refs) > maxUpstreamReferences {
		refs = refs[:maxUpstreamReferences]
	}

	result := UpstreamContext{Thread: thread}
	for _, ref := range refs {
		issue, ferr := src.GetReferencedIssue(ctx, ref.Repo, ref.Number)
		if ferr != nil {
			if errors.Is(ferr, ErrUpstreamIssueNotFound) || errors.Is(ferr, ErrUpstreamIssueTransferred) {
				result.References = append(result.References, UpstreamReferencedIssue{Reference: ref})
				continue
			}
			return UpstreamContext{}, fmt.Errorf("core: fetch upstream context %s#%d: referenced issue %s#%d: %w", repo, number, ref.Repo, ref.Number, ferr)
		}
		result.References = append(result.References, UpstreamReferencedIssue{Reference: ref, Issue: issue, Fetched: true})
	}
	return result, nil
}
