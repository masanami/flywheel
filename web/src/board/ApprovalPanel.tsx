import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/proposed.ts";
import type { ApprovalSummary } from "../api/types.gen.ts";

// 承認のボタンを出すコード。課題の状態の文字列では絞らない。
export const APPROVE_PLAN = "approve_plan";
export const APPROVE_COMPLETION = "approve_completion";

export function approvalKindOf(
  actions: string[],
): "plan" | "completion" | null {
  if (actions.includes(APPROVE_PLAN)) return "plan";
  if (actions.includes(APPROVE_COMPLETION)) return "completion";
  return null;
}

const HELD_NOTE =
  "本番反映（release）は保留のまま残ります。この画面の承認では本番に届きません。";
const HEAD_MISSING = "取得できませんでした";
const RELOAD_NOTE =
  "操作を受け付けられませんでした。ページを再読み込みしてからやり直してください（自動では再送しません）。";
const CONFLICT_NOTE =
  "承認の対象が更新されていました。最新の要約を読み直しました。内容を確かめて、もう一度ボタンを押してください。";

type Phase =
  | { kind: "loading" }
  | { kind: "ready"; summary: ApprovalSummary }
  | { kind: "error"; message: string }
  | { kind: "done"; summary: ApprovalSummary };

export function ApprovalPanel({
  workspace,
  challengeId,
  kind,
  onApproved,
}: {
  workspace: string;
  challengeId: string;
  kind: "plan" | "completion";
  onApproved: () => void;
}) {
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // ① は開いたときに 1 回だけ読む（ライブ更新では読み直さない。版は開いたときのものを ② に添える）。
  useEffect(() => {
    let cancelled = false;
    api.approvalSummary(workspace, challengeId).then(
      (r) => {
        if (!cancelled) setPhase({ kind: "ready", summary: r.approval });
      },
      (e) => {
        if (!cancelled) setPhase({ kind: "error", message: messageOf(e) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [workspace, challengeId]);

  const approve = async (summary: ApprovalSummary) => {
    setBusy(true);
    setNotice(null);
    try {
      await api.approve(workspace, challengeId, summary.version);
      if (!alive.current) return;
      setPhase({ kind: "done", summary });
      onApproved();
    } catch (e) {
      if (!alive.current) return;
      if (e instanceof ApiError && e.code === "conflict") {
        // 自動で再送しない。最新の ① を読み直して示し、もう一度押すことを求める。
        try {
          const r = await api.approvalSummary(workspace, challengeId);
          if (!alive.current) return;
          setPhase({ kind: "ready", summary: r.approval });
          setNotice(CONFLICT_NOTE);
        } catch (e2) {
          if (!alive.current) return;
          setPhase({ kind: "error", message: messageOf(e2) });
        }
      } else if (
        e instanceof ApiError &&
        (e.status === 403 || e.code === "forbidden_origin")
      ) {
        setNotice(RELOAD_NOTE);
      } else {
        setNotice(messageOf(e));
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  // ラベルは ① が返した種類（サーバが現在の状態から決めたもの）に合わせる。読み込み前は prop の種類。
  const shown =
    phase.kind === "ready" || phase.kind === "done" ? phase.summary.kind : kind;
  const buttonLabel =
    shown === "plan" ? "計画を承認する" : "完了を承認する（本番反映は保留）";

  return (
    <section
      aria-label="承認"
      className="approval-panel"
      data-testid="approval-panel"
    >
      <h3>{shown === "plan" ? "計画の承認" : "完了の承認"}</h3>
      {phase.kind === "loading" && <p>承認の要約を読み込み中...</p>}
      {phase.kind === "error" && (
        <p className="approval-control-error" role="alert">
          {phase.message}
        </p>
      )}
      {(phase.kind === "ready" || phase.kind === "done") && (
        <Summary summary={phase.summary} />
      )}
      {phase.kind === "ready" && (
        <div className="approval-control">
          <button
            type="button"
            className="approval-control-confirm"
            disabled={busy}
            onClick={() => approve(phase.summary)}
          >
            {buttonLabel}
          </button>
        </div>
      )}
      {phase.kind === "done" && <output>承認しました。</output>}
      {notice && (
        <p className="approval-control-error" role="alert">
          {notice}
        </p>
      )}
    </section>
  );
}

function messageOf(e: unknown): string {
  if (
    e instanceof ApiError &&
    (e.status === 403 || e.code === "forbidden_origin")
  )
    return RELOAD_NOTE;
  return e instanceof Error ? e.message : "承認の要約を読めませんでした";
}

// ① の要約。完了では、同時には承認されない不可逆操作・release の PR の head・保留のまま残ることを示す。
function Summary({ summary }: { summary: ApprovalSummary }) {
  const completion = summary.kind === "completion";
  return (
    <div className="approval-summary" data-testid="approval-summary">
      <p>
        {summary.title}（{summary.challenge_id}・版 {summary.version}）
      </p>
      {summary.done_criteria && (
        <>
          <h4>完了条件</h4>
          <p className="card-detail-multiline">{summary.done_criteria}</p>
        </>
      )}
      {summary.plan && (
        <>
          <h4>承認する計画（v{summary.plan.version}）</h4>
          <pre style={{ whiteSpace: "pre-wrap" }}>{summary.plan.body}</pre>
        </>
      )}
      {completion && (
        <>
          <h4>同時には承認されない不可逆操作</h4>
          {summary.other_pending_operations.length === 0 ? (
            <p>なし</p>
          ) : (
            <ul>
              {summary.other_pending_operations.map((o) => (
                <li key={o.id}>
                  {o.id} / {o.kind}: {o.summary}
                </li>
              ))}
            </ul>
          )}
          <h4>本番反映（release）の PR</h4>
          {summary.releases.length === 0 ? (
            <p>なし</p>
          ) : (
            <ul>
              {summary.releases.map((r) => (
                <li key={r.id} data-release={r.id}>
                  {r.id}: {r.summary}
                  {r.ref ? `（${r.ref}）` : ""} ・ head:{" "}
                  {r.head_sha ? <code>{r.head_sha}</code> : HEAD_MISSING}
                </li>
              ))}
            </ul>
          )}
          <p data-testid="release-held">{HELD_NOTE}</p>
        </>
      )}
    </div>
  );
}
