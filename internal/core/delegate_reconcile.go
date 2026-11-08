package core

// このファイルは委譲の run が終わった後の照合（親要件チケット #98 §合流と照合。
// 決定 M3P15・M3P41・クリティカル設計決定 6）を持つ。
//
// 照合は子の報告に依らない。リモートのブランチと、そのブランチを head とする PR は
// UpstreamBranchSource（gh の GET）が返した値だけを run の成果物に記録する。照合先の
// ブランチは、報告にあればそのブランチ、無ければスロットの作業ツリーの現在のブランチ。
// 照合は launch_failed を除くすべての結果の後に行い、取得元の課題へは何も書き戻さない。
//
// 成果物の種類が pr で結末が completed の報告は、ブランチの有無に関わらず pr_urls の PR も
// 1 件ずつ調べる（マージ後にブランチが消えても、成果物の PR が委譲先と別のリポジトリに
// あっても見つけるため）。pr_urls は調べる先を示す子の申告にすぎず、記録するのは gh が返した
// PR の値だけ。調べるのは宣言（connectors.json の repos[]）にあるリポジトリの PR に限る。
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
	// UnchangedBranch は、報告にブランチが無く、スロットの現在のブランチが割り当て時と
	// 同じだったために調べなかったブランチ名。
	UnchangedBranch string
	// BranchExists はリモートにそのブランチがあるか（調べられなかったときは false）。
	BranchExists bool
	// PRs は委譲先のリポジトリの PR（ブランチを head とするものと、pr_urls で見つかったもの。
	// URL の重複なし）。
	PRs []UpstreamPullRequest
	// Reported は報告の pr_urls の 1 件ごとの調べた結果（重複を除いた順）。
	Reported []reportedPR
	// Errors は取得できなかったものの説明（gh の失敗・スロットの検査の失敗）。
	Errors []string
	// AttentionReason が空でなければ、スロットを needs_attention にする理由。
	AttentionReason string
}

// maxReportedPRLookups は、報告の pr_urls から 1 回の照合で gh に問い合わせる PR の件数の上限
// （重複を除いた件数。超えた分は調べない）。
const maxReportedPRLookups = 20

// reportedPR は報告の pr_urls の 1 件を調べた結果。
type reportedPR struct {
	// Ref は子が書いた文字列。結果の説明にだけ使い、記録しない。
	Ref string
	// Repo は宣言のリポジトリ（調べなかったときは零値）。Other は委譲先と別のリポジトリか。
	Repo  ConnectorRepo
	Other bool
	// PR は gh が返した PR（取得できたときだけ）。
	PR *UpstreamPullRequest
	// Skipped は調べなかった理由（人に見せる文）、Err は取得の失敗。
	Skipped string
	Err     string
}

// gatherReconciliation はスロットの作業ツリーを検査し、ブランチと PR を取得する。
// result・report は委譲の run の結果と検査を通った報告（報告が無ければ nil）。
func (s *Store) gatherReconciliation(ctx context.Context, in DelegateInput, dc *delegationContext, slotPath, baseBranch string, result RunResult, report *DelegationReport) *reconciliation {
	var reportBranch *string
	if report != nil {
		reportBranch = report.Branch
	}
	rec := s.gatherBranchReconciliation(ctx, in, dc, slotPath, baseBranch, reportBranch)
	if result == RunResultSucceeded && report != nil && report.Outcome == DelegationOutcomeCompleted && dc.Operation.Artifacts == "pr" {
		s.gatherReportedPRs(ctx, in, dc, rec, report.PRURLs)
	}
	return rec
}

// gatherBranchReconciliation はスロットの作業ツリーを検査し、ブランチと、それを head とする
// PR を取得する。reportBranch は報告のブランチ（無ければ nil）。
func (s *Store) gatherBranchReconciliation(ctx context.Context, in DelegateInput, dc *delegationContext, slotPath, baseBranch string, reportBranch *string) *reconciliation {
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
		resumedBranch := dc.Resume != nil && dc.Resume.Branch != "" && st.Branch == dc.Resume.Branch
		if st.Branch != baseBranch || resumedBranch {
			// 割り当て時と異なる現在のブランチは今回の委譲のもの。再開では、割り当て時の
			// ブランチが再開元の（同じ課題の）ブランチなら、そのまま今回のもの。
			rec.Branch, rec.BranchFrom = st.Branch, "slot"
		} else {
			// 現在のブランチが割り当て時と同じ: 子はブランチを作らなかった（前の課題の
			// ブランチが残っているだけ）。今回の成果物として調べない。
			rec.UnchangedBranch = st.Branch
		}
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

// gatherReportedPRs は報告の pr_urls を 1 件ずつ調べて rec に足す。URL は
// parseReleasePullRequestRef で repo と番号に分け、repo を宣言の repos[] の remote と大文字小文字を
// 無視して突き合わせる。宣言にない・形が不正なものは gh を呼ばずに理由を残す。宣言にあるものは
// その repo で PR を引き、委譲先の PR は rec.PRs（ブランチで見つけた PR と URL の重複なし）へ、
// 別のリポジトリの PR は rec.Reported へ置く。
func (s *Store) gatherReportedPRs(ctx context.Context, in DelegateInput, dc *delegationContext, rec *reconciliation, urls []string) {
	seen := map[string]bool{}
	lookups := 0
	for _, raw := range urls {
		if len(rec.Reported) >= 2*maxReportedPRLookups {
			break // 結果の説明が際限なく長くならないよう、記録する件数にも上限を置く
		}
		ref := strings.TrimSpace(raw)
		item := reportedPR{Ref: ref}
		repoName, number, ok := parseReleasePullRequestRef(ref)
		if !ok {
			item.Skipped = "URL の形が不正（GitHub の PR の URL ではない）"
			rec.Reported = append(rec.Reported, item)
			continue
		}
		key := fmt.Sprintf("%s#%d", strings.ToLower(repoName), number)
		if seen[key] {
			continue
		}
		seen[key] = true
		var decl *ConnectorRepo
		if in.ConnDecl != nil {
			for i := range in.ConnDecl.Repos {
				if strings.EqualFold(in.ConnDecl.Repos[i].Remote, repoName) {
					decl = &in.ConnDecl.Repos[i]
					break
				}
			}
		}
		if decl == nil {
			item.Skipped = "リポジトリが宣言（connectors.json）にない"
			rec.Reported = append(rec.Reported, item)
			continue
		}
		item.Repo, item.Other = *decl, !strings.EqualFold(decl.Remote, dc.Repo.Remote)
		if lookups >= maxReportedPRLookups {
			item.Skipped = fmt.Sprintf("調べる件数の上限（%d 件）を超えた", maxReportedPRLookups)
			rec.Reported = append(rec.Reported, item)
			continue
		}
		lookups++
		pr, err := in.Reconcile.GetPullRequest(ctx, decl.Remote, number)
		if err == nil {
			// gh が返した PR が、求めた PR（宣言のリポジトリの同じ番号）でなければ記録しない。
			if gotRepo, gotNumber, ok := parseReleasePullRequestRef(pr.URL); !ok || gotNumber != number || !strings.EqualFold(gotRepo, decl.Remote) {
				err = fmt.Errorf("the pull request gh returned does not match the one requested (%s#%d): %s", decl.Remote, number, oneLine(pr.URL))
			}
		}
		if err != nil {
			item.Err = oneLine(err.Error())
			rec.Reported = append(rec.Reported, item)
			continue
		}
		item.PR = &pr
		rec.Reported = append(rec.Reported, item)
		if !item.Other {
			dup := false
			for _, have := range rec.PRs {
				if strings.EqualFold(have.URL, pr.URL) {
					dup = true
				}
			}
			if !dup {
				rec.PRs = append(rec.PRs, pr)
			}
		}
	}
}

// otherRepoPRs は報告の pr_urls で見つかった、委譲先と別のリポジトリの PR（gh が返した値）。
func (r *reconciliation) otherRepoPRs() []reportedPR {
	var out []reportedPR
	for _, rp := range r.Reported {
		if rp.Other && rp.PR != nil {
			out = append(out, rp)
		}
	}
	return out
}

// otherRepoMergeNote は、委譲先と別のリポジトリの既定ブランチへマージ済みの PR があるときの
// 注記（保留にはしない）。無ければ空。
func (r *reconciliation) otherRepoMergeNote() string {
	var urls []string
	for _, rp := range r.otherRepoPRs() {
		if rp.PR.State == string(artifactStateMerged) && rp.PR.Base == rp.Repo.DefaultBranch {
			urls = append(urls, fmt.Sprintf("%s (%s, base %s)", rp.PR.URL, rp.Repo.Remote, rp.PR.Base))
		}
	}
	if len(urls) == 0 {
		return ""
	}
	return "pull requests of another repository were already merged into its default branch (not held): " + strings.Join(urls, ", ")
}

// artifactInputs は run の成果物として記録する行（照合で確かめられたものだけ）。
func (r *reconciliation) artifactInputs(verifiedAt *time.Time) []insertRunArtifactInput {
	var out []insertRunArtifactInput
	if r.BranchExists {
		out = append(out, insertRunArtifactInput{Kind: artifactKindBranch, Ref: r.Branch, VerifiedAt: verifiedAt})
	}
	recorded := map[string]bool{}
	add := func(pr UpstreamPullRequest) {
		if recorded[strings.ToLower(pr.URL)] {
			return
		}
		recorded[strings.ToLower(pr.URL)] = true
		out = append(out, insertRunArtifactInput{
			Kind: artifactKindPR, Ref: pr.URL, State: artifactState(pr.State), Base: pr.Base, VerifiedAt: verifiedAt,
		})
	}
	for _, pr := range r.PRs {
		add(pr)
	}
	for _, rp := range r.otherRepoPRs() {
		add(*rp.PR)
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
		if r.UnchangedBranch != "" {
			fmt.Fprintf(&b, "- 調べたブランチ: なし（報告にブランチが無く、スロットの現在のブランチ %s は割り当て時から変わっていないため、子はブランチを作らなかったとみなした）\n", r.UnchangedBranch)
		} else {
			b.WriteString("- 調べたブランチ: なし（報告にブランチが無く、スロットの現在のブランチも得られない）\n")
		}
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
	for _, rp := range r.Reported {
		ref := oneLine(rp.Ref)
		if len([]rune(ref)) > 200 {
			ref = string([]rune(ref)[:200]) + "…"
		}
		switch {
		case rp.PR != nil && rp.Other:
			fmt.Fprintf(&b, "- 報告の PR（委譲先と別のリポジトリ %s）: %s（状態: %s、base: %s）\n", rp.Repo.Remote, rp.PR.URL, rp.PR.State, rp.PR.Base)
		case rp.PR != nil:
			fmt.Fprintf(&b, "- 報告の PR（委譲先のリポジトリ %s）: %s（状態: %s、base: %s）\n", rp.Repo.Remote, rp.PR.URL, rp.PR.State, rp.PR.Base)
		case rp.Skipped != "":
			fmt.Fprintf(&b, "- 報告の PR: %s（調べなかった: %s）\n", ref, rp.Skipped)
		default:
			fmt.Fprintf(&b, "- 報告の PR: %s（%s の取得の失敗: %s）\n", ref, rp.Repo.Remote, rp.Err)
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
	rec *reconciliation, result RunResult, report *DelegationReport, cycleID string) (status *string, note string, err error) {
	runIDInt, _ := parseRunID(runIDDisplay)

	// release の登録。委譲先のリポジトリの PR は、base が宣言の既定ブランチの PR（状態を問わない）
	// ごとに、同じ PR の URL の release が無ければ登録する。委譲先と別のリポジトリの PR は、
	// open で base がそのリポジトリの宣言の既定ブランチのものだけを登録する（マージ済みの PR は
	// 登録しない。マージの承認の口に載せられるのは、まだマージされていない PR だけ）。
	var releasePRs []UpstreamPullRequest
	for _, pr := range rec.PRs {
		if pr.Base == dc.Repo.DefaultBranch {
			releasePRs = append(releasePRs, pr)
		}
	}
	for _, rp := range rec.otherRepoPRs() {
		if rp.PR.State == string(artifactStateOpen) && rp.PR.Base == rp.Repo.DefaultBranch {
			releasePRs = append(releasePRs, *rp.PR)
		}
	}
	for _, pr := range releasePRs {
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

	// 承認なしの本番反映の検出（M3P15）。委譲先のリポジトリだけが対象（委譲先と別のリポジトリへ
	// マージ済みの PR は保留にせず、結果の注記で見えるようにする）。
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
		return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, q.String(), holdReasonUnapprovedMerge, cycleID)
	}

	// 子の問い・停止の報告（J4 は S3）。保留の原因の run はこの run になる。
	if result == RunResultSucceeded && report != nil {
		switch report.Outcome {
		case DelegationOutcomeQuestions:
			return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, formatQuestionsHold(report), holdReasonQuestions, cycleID)
		case DelegationOutcomeBlocked:
			return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, formatBlockedHold(report), holdReasonBlocked, cycleID)
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
		for _, rp := range rec.otherRepoPRs() {
			if rp.PR.State == string(artifactStateOpen) || rp.PR.State == string(artifactStateMerged) {
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
		status, note, err = s.mapReconciliation(ctx, dc, runIDDisplay, OpSubmit, "", holdReasonNone, cycleID)
		if err == nil && status != nil && note == "" {
			note = rec.otherRepoMergeNote()
		}
		return status, note, err
	}
	question := "子は完了を報告したが成果物が見つからない（宣言の成果物の種類: " + dc.Operation.Artifacts + "）。\n" + rec.describe()
	return s.mapReconciliation(ctx, dc, runIDDisplay, OpHold, question, holdReasonArtifactMissing, cycleID)
}

// releaseApproved は課題の、参照が prURL の release が承認済みかを返す。
// 実行済み（executed）の release も承認済みとして数える（実行済みの PR を、承認なしの
// 本番反映と誤検出しない）。
func (s *Store) releaseApproved(ctx context.Context, challengeID int64, prURL string) (bool, error) {
	var approved bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ? AND state IN (?, ?)`,
			challengeID, string(OperationKindRelease), prURL, string(OperationStateApproved), string(OperationStateExecuted)).Scan(&n); err != nil {
			return err
		}
		approved = n > 0
		return nil
	})
	return approved, classifyReadWriteErr(err)
}

// mapReconciliation は課題が着手中のときだけ、経路 invoker・原因の run の ID つきで
// submit（検証中）または hold（人間対応待ち。question が問い）を行う。
//
// reason は保留の理由の種別で、発生（M45P5）の種類を決める。保留の行と同じトランザクションで
// 発生を作る。holdReasonArtifactMissing（成果物が見つからない）は発生を作らない。
// cycleID はこの写像が属する周（発生が持つ周の ID）。
func (s *Store) mapReconciliation(ctx context.Context, dc *delegationContext, runIDDisplay string, op Operation, question string, reason holdReason, cycleID string) (*string, string, error) {
	_, err := s.transitionAsInvoker(ctx, runIDDisplay, dc.Challenge.ID, op, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusInProgress {
			return errReconcileNotEligible
		}
		if op == OpHold {
			return s.insertHoldWithOccurrence(ctx, tc, question, reason, cycleID)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return nil, s.reconcileSkippedNote(ctx, dc), nil
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

// reconcileSkippedNote は、写す直前に課題が着手中でなくなっていて写さなかった場合の注記を返す。
// 課題が取り下げられていたときだけ、取り下げを検出して写さなかったことを返す（振る舞いは変えない）。
// それ以外の理由・読めなかったときは空。
func (s *Store) reconcileSkippedNote(ctx context.Context, dc *delegationContext) string {
	cid, ok := parseChallengeID(dc.Challenge.ID)
	if !ok {
		return ""
	}
	var status Status
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if c != nil {
			status = c.Status
		}
		return nil
	})
	if err != nil || status != StatusWithdrawn {
		return ""
	}
	return "the challenge was withdrawn before the reconciliation was mapped; nothing was mapped"
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
