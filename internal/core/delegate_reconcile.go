package core

// このファイルは委譲の run が終わった後の照合（親要件チケット #98 §合流と照合。
// 決定 M3P15・M3P41・クリティカル設計決定 6）を持つ。
//
// 照合は子の報告に依らない。リモートのブランチと、そのブランチを head とする PR は
// UpstreamBranchSource（gh の GET）が返した値だけを run の成果物に記録する。照合先の
// ブランチは、報告にあればそのブランチ、無ければスロットの作業ツリーの現在のブランチ。
// 照合は launch_failed を除くすべての結果の後に行い、取得元の課題へは何も書き戻さない。
//
// 流れ（launchDelegation から呼ぶ）:
//
//	① gatherReconciliation: スロットの作業ツリーの検査と gh の GET（トランザクションの外）
//	② run の終了・成果物の記録・スロットの解放（または needs_attention）を 1 つの
//	   トランザクションで行う（launchDelegation 側）
//	③ applyReconciliation: release の登録 → 承認なしの本番反映の検出 → 結末 questions・
//	   blocked の保留 → 成果物の確認と検証中・人間対応待ちへの写像

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// reconciliation は照合で調べた事実。
type reconciliation struct {
	// Branch は調べたブランチ名。BranchFrom は "report" | "slot" | ""（調べる先が無い）。
	Branch     string
	BranchFrom string
	// BranchExists はリモートにそのブランチがあるか（調べられなかったときは false）。
	BranchExists bool
	PRs          []UpstreamPullRequest
	// Errors は取得できなかったものの説明（gh の失敗・スロットの検査の失敗）。
	Errors []string
	// AttentionReason が空でなければ、スロットを needs_attention にする理由。
	AttentionReason string
}

// gatherReconciliation はスロットの作業ツリーを検査し、ブランチと PR を取得する。
// reportBranch は報告のブランチ（無ければ nil）。
func (s *Store) gatherReconciliation(ctx context.Context, in DelegateInput, dc *delegationContext, slotPath string, reportBranch *string) *reconciliation {
	rec := &reconciliation{}
	tree := SlotTree{Path: slotPath}
	if dc.Repo.Slots.Provider == string(slotProviderWorktree) {
		tree.BaseClone = filepath.Join(s.workspace, dc.Repo.Slots.Base)
	}
	st, err := in.Git.Inspect(ctx, tree)
	switch {
	case err != nil:
		rec.AttentionReason = "inspecting the working tree after the delegation failed: " + oneLine(err.Error())
		rec.Errors = append(rec.Errors, rec.AttentionReason)
	case !st.Exists:
		rec.AttentionReason = "the slot path does not exist after the delegation: " + slotPath
	case !st.PointerOK:
		rec.AttentionReason = "the working tree's .git pointer is broken after the delegation: " + oneLine(st.PointerProblem)
	case st.Dirty:
		rec.AttentionReason = "the working tree has uncommitted changes after the delegation"
	}

	if reportBranch != nil && strings.TrimSpace(*reportBranch) != "" {
		rec.Branch, rec.BranchFrom = strings.TrimSpace(*reportBranch), "report"
	} else if err == nil && st.Branch != "" {
		rec.Branch, rec.BranchFrom = st.Branch, "slot"
	}
	if rec.Branch == "" {
		return rec
	}
	if !plausibleBranchName(rec.Branch) {
		// 子の報告のブランチ名は検証されていない。gh の URL の経路へ入れない（".." などで
		// 別のリソースを指させない）。
		rec.Errors = append(rec.Errors, fmt.Sprintf("the branch name %q is not a valid git branch name; it was not looked up", rec.Branch))
		rec.Branch, rec.BranchFrom = "", ""
		return rec
	}
	remote := dc.Repo.Remote
	exists, err := in.Reconcile.BranchExists(ctx, remote, rec.Branch)
	if err != nil {
		rec.Errors = append(rec.Errors, "looking up the remote branch failed: "+oneLine(err.Error()))
	}
	rec.BranchExists = exists
	prs, err := in.Reconcile.ListPullRequestsByHead(ctx, remote, rec.Branch)
	if err != nil {
		rec.Errors = append(rec.Errors, "looking up the pull requests failed: "+oneLine(err.Error()))
	}
	rec.PRs = prs
	return rec
}

// artifactInputs は run の成果物として記録する行（照合で確かめられたものだけ）。
func (r *reconciliation) artifactInputs(verifiedAt *time.Time) []insertRunArtifactInput {
	var out []insertRunArtifactInput
	if r.BranchExists {
		out = append(out, insertRunArtifactInput{Kind: artifactKindBranch, Ref: r.Branch, VerifiedAt: verifiedAt})
	}
	for _, pr := range r.PRs {
		out = append(out, insertRunArtifactInput{
			Kind: artifactKindPR, Ref: pr.URL, State: artifactState(pr.State), Base: pr.Base, VerifiedAt: verifiedAt,
		})
	}
	return out
}

// describe は人間対応待ちの問いに書く照合の結果。
func (r *reconciliation) describe() string {
	var b strings.Builder
	b.WriteString("照合の結果:\n")
	switch r.BranchFrom {
	case "report":
		fmt.Fprintf(&b, "- 調べたブランチ: %s（報告のブランチ）\n", r.Branch)
	case "slot":
		fmt.Fprintf(&b, "- 調べたブランチ: %s（報告にブランチが無いため、スロットの現在のブランチ）\n", r.Branch)
	default:
		b.WriteString("- 調べたブランチ: なし（報告にブランチが無く、スロットの現在のブランチも得られない）\n")
	}
	if r.Branch != "" {
		fmt.Fprintf(&b, "- リモートのブランチ: %s\n", map[bool]string{true: "ある", false: "見つからない"}[r.BranchExists])
		if len(r.PRs) == 0 {
			b.WriteString("- このブランチを head とする PR: 見つからない\n")
		}
		for _, pr := range r.PRs {
			fmt.Fprintf(&b, "- PR: %s（状態: %s、base: %s）\n", pr.URL, pr.State, pr.Base)
		}
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "- 取得の失敗: %s\n", e)
	}
	return strings.TrimRight(b.String(), "\n")
}

var errReconcileNotEligible = errors.New("core: the challenge is no longer in progress")

// applyReconciliation は照合の結果を課題へ写す。release の登録 → 承認なしの本番反映の
// 検出（成果物の確認より優先）→ 成果物の確認（結果が succeeded で結末が completed の
// ときだけ）の順。写す直前に読み直した課題が着手中でなければ、写さない。
func (s *Store) applyReconciliation(ctx context.Context, dc *delegationContext, runIDDisplay string,
	rec *reconciliation, result RunResult, report *DelegationReport) (status *string, note string, err error) {
	runIDInt, _ := parseRunID(runIDDisplay)

	// release の登録。base が宣言の既定ブランチの PR（状態を問わない）ごとに、同じ PR の
	// URL の release が無ければ登録する。
	for _, pr := range rec.PRs {
		if pr.Base != dc.Repo.DefaultBranch {
			continue
		}
		ref := pr.URL
		summary := pr.Title
		if strings.TrimSpace(summary) == "" {
			summary = pr.URL // 要約は空にできないので、タイトルの無い PR は URL を要約にする
		}
		_, cerr := s.createOperation(ctx, ChannelInvoker, &runIDInt, true, OperationInput{
			ChallengeID: dc.Challenge.ID, Kind: string(OperationKindRelease), Summary: summary, Ref: &ref,
		})
		if cerr != nil && !errors.Is(cerr, errOperationDuplicate) && !errors.Is(cerr, ErrTerminalState) {
			return nil, "", cerr
		}
	}

	// 承認なしの本番反映の検出（M3P15）。
	cid, _ := parseChallengeID(dc.Challenge.ID)
	var unapproved []UpstreamPullRequest
	for _, pr := range rec.PRs {
		if pr.State != string(artifactStateMerged) || pr.Base != dc.Repo.DefaultBranch {
			continue
		}
		approved, aerr := s.releaseApproved(ctx, cid, pr.URL)
		if aerr != nil {
			return nil, "", aerr
		}
		if !approved {
			unapproved = append(unapproved, pr)
		}
	}
	if len(unapproved) > 0 {
		var q strings.Builder
		fmt.Fprintf(&q, "承認なしの本番反映を検出した: 既定ブランチ（%s）へマージ済みの PR の release が承認されていない。\n", dc.Repo.DefaultBranch)
		for _, pr := range unapproved {
			fmt.Fprintf(&q, "- %s\n", pr.URL)
		}
		q.WriteString(rec.describe())
		return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, q.String())
	}

	// 子の問い・停止の報告（J4 は S3）。保留の原因の run はこの run になる。
	if result == RunResultSucceeded && report != nil {
		switch report.Outcome {
		case DelegationOutcomeQuestions:
			return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, formatQuestionsHold(report))
		case DelegationOutcomeBlocked:
			return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, formatBlockedHold(report))
		}
	}

	if result != RunResultSucceeded || report == nil || report.Outcome != DelegationOutcomeCompleted {
		return nil, note, nil
	}

	confirmed := false
	switch dc.Operation.Artifacts {
	case "pr":
		for _, pr := range rec.PRs {
			if pr.State == string(artifactStateOpen) || pr.State == string(artifactStateMerged) {
				confirmed = true
			}
		}
	case "branch":
		// 既定ブランチ自体は子が作った成果物ではない（作業ツリーが既定ブランチのままなら、
		// リモートにあっても確かめられたことにしない）。
		confirmed = rec.BranchExists && rec.Branch != dc.Repo.DefaultBranch
	default: // "none"、および宣言の無い操作: 照合で確かめる成果物が無い（中身は J5 が判定する）
		confirmed = true
	}
	if confirmed {
		return s.mapReconciliation(ctx, dc, runIDDisplay, OpSubmit, "")
	}
	question := "子は完了を報告したが成果物が見つからない（宣言の成果物の種類: " + dc.Operation.Artifacts + "）。\n" + rec.describe()
	return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, question)
}

// releaseApproved は課題の、参照が prURL の release が承認済みかを返す。
func (s *Store) releaseApproved(ctx context.Context, challengeID int64, prURL string) (bool, error) {
	var approved bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ? AND state = ?`,
			challengeID, string(OperationKindRelease), prURL, string(OperationStateApproved)).Scan(&n); err != nil {
			return err
		}
		approved = n > 0
		return nil
	})
	return approved, classifyReadWriteErr(err)
}

// mapReconciliation は課題が着手中のときだけ、経路 invoker・原因の run の ID つきで
// submit（検証中）または hold（人間対応待ち。question が問い）を行う。
func (s *Store) mapReconciliation(ctx context.Context, dc *delegationContext, runIDDisplay string, op Operation, question string) (*string, string, error) {
	_, err := s.transitionAsInvoker(ctx, runIDDisplay, dc.Challenge.ID, op, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusInProgress {
			return errReconcileNotEligible
		}
		if op == OpHold {
			return insertHold(ctx, tc, question)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if op == OpHold {
		v := string(StatusAwaitingHuman)
		return &v, "", nil
	}
	v := string(StatusVerifying)
	return &v, "", nil
}

// plausibleBranchName は git のブランチ名として成り立つ最低限の形（git check-ref-format の
// 主な禁止事項）かを返す。
func plausibleBranchName(b string) bool {
	if b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") ||
		strings.Contains(b, "//") || strings.Contains(b, "@{") {
		return false
	}
	for _, seg := range strings.Split(b, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	for _, r := range b {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	return true
}
