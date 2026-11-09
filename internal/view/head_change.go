package view

import "github.com/masanami/flywheel/internal/core"

// 検証の後に PR の head が動いた release の評価（#485）。承認の要約（`approve` の端末の要約・`--relayed --json` の
// 出力）と `show` が、検証の head から今の head までに入ったコミットと、取り込みのマージが直した差分を示す。

// HeadChangeCommit は検証の後に入ったコミット 1 件。Role は merge_in（既定ブランチを取り込むマージ）・
// default_branch（既定ブランチ側のコミット）・pr_change（PR 側の取り込み以外のコミット）。
type HeadChangeCommit struct {
	Role    string `json:"role"`
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// ConflictFixFile は取り込みのマージが両方の親と異なる形にしたファイル 1 件と、検証した版からの差分。
// 上流が差分を返さなかったファイルは patch が null で patch_unavailable が true。
type ConflictFixFile struct {
	Filename         string  `json:"filename"`
	Patch            *string `json:"patch"`
	PatchUnavailable bool    `json:"patch_unavailable"`
}

// ConflictFix は取り込みのマージ 1 件が直したファイルの一覧。
type ConflictFix struct {
	Files       []ConflictFixFile `json:"files"`
	MergeCommit string            `json:"merge_commit"`
}

// HeadChange は release の PR の head が検証の head から動いたときの評価。
// kind は merge_in_only（取り込みだけ）・content_changed（取り込み以外のコミットが入った）・
// undeterminable（force-push・打ち切り・取得の失敗などで判定できない）。
type HeadChange struct {
	ChecksPassed  bool               `json:"checks_passed"`
	Commits       []HeadChangeCommit `json:"commits"`
	ConflictFixes []ConflictFix      `json:"conflict_fixes"`
	// Detail は区分の根拠（取り込み以外のコミット・判定できなかった理由）。merge_in_only は空。
	Detail string `json:"detail"`
	// Head は評価した今の head（完全形）、VerifiedHead は比べた検証の head（完全形）。
	Head         string `json:"head"`
	Kind         string `json:"kind"`
	VerifiedHead string `json:"verified_head"`
}

// FromReleaseHeadAssessment は core.ReleaseHeadAssessment を HeadChange へ変換する。
func FromReleaseHeadAssessment(a *core.ReleaseHeadAssessment) HeadChange {
	out := HeadChange{
		ChecksPassed:  a.ChecksPassed,
		Commits:       []HeadChangeCommit{},
		ConflictFixes: []ConflictFix{},
		Detail:        a.Change.Detail,
		Head:          a.HeadSHA,
		Kind:          string(a.Change.Kind),
		VerifiedHead:  a.VerifiedHead,
	}
	for _, c := range a.Change.Commits {
		out.Commits = append(out.Commits, HeadChangeCommit{Role: string(c.Role), SHA: c.SHA, Subject: c.Subject})
	}
	for _, fix := range a.Change.ConflictFixes {
		f := ConflictFix{MergeCommit: fix.MergeCommit, Files: []ConflictFixFile{}}
		for _, file := range fix.Files {
			cf := ConflictFixFile{Filename: file.Filename, PatchUnavailable: file.PatchUnavailable}
			if !file.PatchUnavailable {
				p := file.Patch
				cf.Patch = &p
			}
			f.Files = append(f.Files, cf)
		}
		out.ConflictFixes = append(out.ConflictFixes, f)
	}
	return out
}

// ReleaseHeadChange は `show` が、承認待ちの release のうち PR の head が検証の head から動いたものについて返す評価。
type ReleaseHeadChange struct {
	HeadChange  HeadChange `json:"head_change"`
	OperationID string     `json:"operation_id"`
}
