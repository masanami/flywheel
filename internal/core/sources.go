package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// NormalizeLogin は GitHub の login を比較用に正規化する（大文字小文字を無視し、
// 先頭の "@" を1つだけ除く）。比較（self_assignees とのマッチ判定）そのものは
// #56 の範囲であり、ここでは正規化だけを提供する
// （docs/features/m2-github-issue-ingest.md §取り込みの対象「大文字小文字を
// 無視し、先頭の @ を除いて比較する」）。
func NormalizeLogin(login string) string {
	return strings.ToLower(strings.TrimPrefix(login, "@"))
}

// --- 取り込み元の宣言（.flywheel/sources.json）§IF / API ---
//
// docs/features/m2-github-issue-ingest.md §IF / API「宣言ファイル」に定める形。
// 検証（LoadSourcesDeclaration）は本チケット（#53）の範囲。宣言を core に読ませる
// CLI の結線（存在チェックの終了コードへの写像を含む）は #54 の範囲。

// sourcesFileName はワークスペース直下の宣言ファイル名（storeDBPath・dbFileName と
// 対になる。workspace.go の flywheelDirName を共有する）。
const sourcesFileName = "sources.json"

// sourcesFilePath は base（ワークスペースの基点ディレクトリ）の下の宣言ファイルの
// 絶対パスを返す。
func sourcesFilePath(base string) string {
	return filepath.Join(base, flywheelDirName, sourcesFileName)
}

// sourceIDPattern は宣言の id の閉じた形式（docs/features/m2-github-issue-ingest.md
// §取り込み元の宣言）。
var sourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// repoPattern は "<owner>/<name>" の形（owner・name のどちらもスラッシュ・空白を
// 含まない非空文字列）。
var repoPattern = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// SourceType は取り込み元の種類の閉集合（現時点では github-issue だけ）。
type SourceType string

// SourceTypeGitHubIssue は唯一の SourceType の値。
const SourceTypeGitHubIssue SourceType = "github-issue"

// AssigneePolicy は取り込み元の assignee 絞り込みの閉集合
// （docs/features/m2-github-issue-ingest.md §取り込みの対象）。
type AssigneePolicy string

// AssigneePolicy の2値。
const (
	AssigneePolicySelfOnly      AssigneePolicy = "self-only"
	AssigneePolicyExcludeOthers AssigneePolicy = "exclude-others"
)

var assigneePolicyValues = []AssigneePolicy{AssigneePolicySelfOnly, AssigneePolicyExcludeOthers}

// isKnownAssigneePolicy は s が閉集合（assigneePolicyValues）に完全一致するかを
// 返す。宣言ファイルの値は enums.go の Parse*（前後の空白を trim してから
// 比較する）とは異なり、完全一致だけを受け付ける（self-review 指摘: 宣言に
// 前後の空白を許すと、検証は通るのに Policy() の戻り値がどの閉集合の値とも
// 一致しなくなる）。round 2 self-review 指摘: 以前は closed set をこの関数と
// validate() の両方に書いており、assigneePolicyValues と二重管理になって
// いた。ここへ一本化する。
func isKnownAssigneePolicy(s string) bool {
	for _, v := range assigneePolicyValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// isKnownUrgency は isKnownAssigneePolicy と同じ規則（完全一致）で、s が
// enums.go の urgencyValues に一致するかを返す（ParseUrgency は trim してから
// 比較するため、宣言ファイルの値の検証には使わない）。
func isKnownUrgency(s string) bool {
	for _, v := range urgencyValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// SourceEntry は宣言の1ブロック（取り込み元）。AssigneePolicy はポインタで持ち、
// nil＝省略（既定 exclude-others）と、閉集合外の明示的な値（例: 空文字列）を
// 区別できるようにする（Policy を参照）。
type SourceEntry struct {
	ID             string          `json:"id"`
	Type           SourceType      `json:"type"`
	Repos          []string        `json:"repos"`
	AssigneePolicy *AssigneePolicy `json:"assignee_policy"`
	// SelfAssignees は省略・null なら nil（"gh api user" の login で解決する。
	// §取り込みの対象）。本チケットは受け付けと保持だけを行い、要素の login の
	// 形式検証（空文字列・"@" だけの要素の扱い等）と、nil と空配列 `[]` の
	// 意味の違いの判定は、比較・絞り込みの規則を持つ #56 の範囲とする
	// （self-review 指摘: ここで規則を先取りすると #56 の設計判断を core の
	// 別ファイルが握ってしまう）。
	SelfAssignees []string          `json:"self_assignees"`
	UrgencyLabels map[string]string `json:"urgency_labels"`
}

// Policy は e の実効の assignee_policy を返す（省略時は exclude-others。
// docs/features/m2-github-issue-ingest.md「省略時は exclude-others」）。
// e は LoadSourcesDeclaration が返した検証済みの値であることを前提にする
// （検証前の生の値には呼ばない）。
func (e SourceEntry) Policy() AssigneePolicy {
	if e.AssigneePolicy == nil {
		return AssigneePolicyExcludeOthers
	}
	return *e.AssigneePolicy
}

// SourcesDeclaration は .flywheel/sources.json の中身（LoadSourcesDeclaration の
// 戻り値）。
type SourcesDeclaration struct {
	Version int           `json:"version"`
	Sources []SourceEntry `json:"sources"`
}

// topLevelKeys は宣言の最上位で許される JSON キーの閉集合（大文字小文字の
// 完全一致で照合する。encoding/json の DisallowUnknownFields はフィールド名の
// 大文字小文字を無視して照合するため、"Version" のような大文字違いのキーを
// 素通りさせてしまう。ここで生の JSON オブジェクトのキーを完全一致で
// 検査してから構造体へ渡すことで、大文字小文字違いの重複キーも fail-closed に
// 拒否する（self-review 指摘: encoding/json は "id" と "ID" を同じフィールドの
// 別表記として扱い、後勝ちで静かに上書きする）。
var topLevelKeys = map[string]bool{"version": true, "sources": true}

// sourceEntryKeys は宣言の取り込み元1要素で許される JSON キーの閉集合
// （topLevelKeys と同じ理由・同じ規則）。
var sourceEntryKeys = map[string]bool{
	"id": true, "type": true, "repos": true,
	"assignee_policy": true, "self_assignees": true, "urgency_labels": true,
}

// LoadSourcesDeclaration は base（ワークスペースの基点ディレクトリ。
// Store.Workspace() が返す値、または OpenWorkspace に渡したものと同じ規則で
// 解決した絶対パス）の下の .flywheel/sources.json を読み、検証する。
// base 自身の探索（--workspace → FLYWHEEL_WORKSPACE → カレントディレクトリ
// から親へ遡る）はここでは行わない（呼び出し側が Store.Workspace() 経由で
// 解決済みの値を渡すことを前提にする。self-review 指摘: 呼び出し側が独自の
// ワークスペース解決を再実装しないよう明記する）。
//
// ファイルが無ければ ErrConfigNotFound。JSON として解釈できない・未知のキー
// （最上位・取り込み元の要素それぞれ。大文字小文字違いの重複キーも含む）・
// 型違い・§取り込み元の宣言 の規則（version・id・type・repos・
// assignee_policy・urgency_labels）に反する場合はすべて ErrConfigInvalid
// （fail-closed。何も取得・変更しない）。
func LoadSourcesDeclaration(base string) (*SourcesDeclaration, error) {
	path := sourcesFilePath(base)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrConfigNotFound
		}
		return nil, fmt.Errorf("%w: read %s: %w", ErrConfigInvalid, path, err)
	}

	decl, err := parseSourcesDeclaration(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s: %w", ErrConfigInvalid, path, err)
	}

	if err := decl.validate(); err != nil {
		return nil, err
	}
	return decl, nil
}

// parseSourcesDeclaration は JSON のバイト列を SourcesDeclaration へ変換する。
// 2段階で読む: (1) 生のキーの集合を大文字小文字の完全一致で検査する
// （topLevelKeys・sourceEntryKeys）、(2) 検査済みの断片だけを型付きの構造体へ
// デコードする（DisallowUnknownFields は (1) を通り抜けたキーに対する
// 保険として残す）。
func parseSourcesDeclaration(data []byte) (*SourcesDeclaration, error) {
	// 末尾のゴミの拒否（"{}garbage"・"{}}"・2つ目の JSON 値）と、キーの完全一致
	// 検査に使う生のキー集合の取得は、他の宣言ファイル（agent.json・
	// connectors.json）と共有する jsondecl.go の decodeStrictJSONObject が担う
	// （self-review 指摘: dec.More() だけでは "{...}}" を見逃す。ここは元の
	// 挙動を変えない機械的な抽出）。
	raw, err := decodeStrictJSONObject(data)
	if err != nil {
		return nil, err
	}

	if err := rejectUnknownKeys(raw, topLevelKeys, "top-level"); err != nil {
		return nil, err
	}

	var decl SourcesDeclaration
	if v, ok := raw["version"]; ok {
		if err := json.Unmarshal(v, &decl.Version); err != nil {
			return nil, fmt.Errorf("version: %w", err)
		}
	}

	// "sources" は必須のキー（省略・null は「宣言に不備がある」として
	// fail-closed に拒否する。self-review 指摘: 省略・null を「0件」として
	// 静かに受理すると、設定ミスで宣言の中身が読まれない事故を見逃す）。
	// 明示的な空配列 `[]` は「0件」として引き続き許容する。
	v, ok := raw["sources"]
	if !ok || string(bytes.TrimSpace(v)) == "null" {
		return nil, fmt.Errorf(`missing required key "sources"`)
	}
	var rawSources []map[string]json.RawMessage
	if err := json.Unmarshal(v, &rawSources); err != nil {
		return nil, fmt.Errorf("sources: %w", err)
	}

	decl.Sources = make([]SourceEntry, 0, len(rawSources))
	for i, rs := range rawSources {
		for k := range rs {
			if !sourceEntryKeys[k] {
				return nil, fmt.Errorf("sources[%d]: unknown key %q", i, k)
			}
		}
		entryJSON, err := json.Marshal(rs)
		if err != nil {
			return nil, fmt.Errorf("sources[%d]: %w", i, err)
		}
		var e SourceEntry
		entryDec := json.NewDecoder(bytes.NewReader(entryJSON))
		entryDec.DisallowUnknownFields()
		if err := entryDec.Decode(&e); err != nil {
			return nil, fmt.Errorf("sources[%d]: %w", i, err)
		}
		decl.Sources = append(decl.Sources, e)
	}

	return &decl, nil
}

// validate は d が §取り込み元の宣言 の規則を満たすかを検査する。最初に見つけた
// 違反を ErrConfigInvalid として返す（fail-closed。宣言全体を検査してすべての
// 違反を集約する必要は無い）。
func (d *SourcesDeclaration) validate() error {
	if d.Version != 1 {
		return fmt.Errorf("%w: version must be 1, got %d", ErrConfigInvalid, d.Version)
	}

	seenIDs := make(map[string]bool, len(d.Sources))
	// seenRepos は大文字小文字を無視した repo（"<owner>/<name>"）をキーにする。
	// GitHub の owner/name は大文字小文字を区別しないため、"O/R" と "o/r" は
	// 同じリポジトリを指す（self-review 指摘）。値は最初に見つかった側の
	// source id とその表記（エラーメッセージ用）。
	type repoOccurrence struct {
		sourceID string
		repo     string
	}
	seenRepos := make(map[string]repoOccurrence, len(d.Sources))
	for i, e := range d.Sources {
		if !sourceIDPattern.MatchString(e.ID) {
			return fmt.Errorf("%w: sources[%d].id %q does not match %s", ErrConfigInvalid, i, e.ID, sourceIDPattern.String())
		}
		if seenIDs[e.ID] {
			return fmt.Errorf("%w: duplicate source id %q", ErrConfigInvalid, e.ID)
		}
		seenIDs[e.ID] = true

		if e.Type != SourceTypeGitHubIssue {
			return fmt.Errorf("%w: source %q has unsupported type %q", ErrConfigInvalid, e.ID, e.Type)
		}

		if len(e.Repos) == 0 {
			return fmt.Errorf("%w: source %q has no repos", ErrConfigInvalid, e.ID)
		}
		for _, r := range e.Repos {
			if !repoPattern.MatchString(r) {
				return fmt.Errorf("%w: source %q has invalid repo %q (want <owner>/<name>)", ErrConfigInvalid, e.ID, r)
			}
			key := strings.ToLower(r)
			if prev, ok := seenRepos[key]; ok {
				return fmt.Errorf("%w: repo %q (source %q) refers to the same repository as %q (source %q)",
					ErrConfigInvalid, r, e.ID, prev.repo, prev.sourceID)
			}
			seenRepos[key] = repoOccurrence{sourceID: e.ID, repo: r}
		}

		// assignee_policy・urgency_labels の値は閉集合との完全一致だけを受け
		// 付ける（enums.go の ParseUrgency 等は前後の空白を trim してから
		// 比較するため、" self-only " のような値が検証を通ってしまい、
		// Policy()・UrgencyLabels の消費者はどの閉集合の値とも一致しなくなる。
		// self-review 指摘。宣言ファイルの値に trim の寛容さは要らない。
		// isKnownAssigneePolicy/isKnownUrgency は既存の閉集合の一覧
		// （assigneePolicyValues・urgencyValues）を1箇所だけ参照する）。
		if e.AssigneePolicy != nil {
			raw := string(*e.AssigneePolicy)
			if !isKnownAssigneePolicy(raw) {
				return fmt.Errorf("%w: source %q has invalid assignee_policy %q", ErrConfigInvalid, e.ID, raw)
			}
		}

		for label, urgency := range e.UrgencyLabels {
			if !isKnownUrgency(urgency) {
				return fmt.Errorf("%w: source %q urgency_labels[%q] = %q is not a valid urgency", ErrConfigInvalid, e.ID, label, urgency)
			}
		}
	}
	return nil
}
