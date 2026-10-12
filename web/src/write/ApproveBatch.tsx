import { useState } from "react";
import { api } from "../api/client.ts";
import type { ChallengeCard, Operation } from "../api/types.gen.ts";
import type { ApprovalPrepared, ApprovalTarget } from "../api/write.ts";
import { ClampedText } from "../cards.tsx";
import { PasskeyDialog, errorText, isConflict } from "./common.tsx";

const KIND_LABEL = {
  plan: "計画の承認",
  completion: "完了の承認",
  operation: "不可逆操作の承認",
} as const;

const key = (t: ApprovalTarget) => `${t.kind}:${t.id}`;

// 同じワークスペースの承認待ちから集合を選び、1 回のパスキーでまとめて承認する（M4H2・M4P20）。
// 集合は確認画面を出した時点（①）で固定する。版が変わっていたら全体を拒否し、読み直して再確認を求める。
export function ApproveBatch({
  workspace,
  cards,
  operations,
}: {
  workspace: string;
  cards: ChallengeCard[];
  operations: Operation[];
}) {
  const waiting = cards.filter(
    (c) =>
      c.status === "awaiting_plan_approval" ||
      c.status === "awaiting_completion_approval",
  );
  const [selected, setSelected] = useState<ApprovalTarget[]>([]);
  const [prepared, setPrepared] = useState<ApprovalPrepared | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const isSel = (t: ApprovalTarget) => selected.some((s) => key(s) === key(t));
  const toggle = (t: ApprovalTarget) =>
    setSelected(
      isSel(t) ? selected.filter((s) => key(s) !== key(t)) : [...selected, t],
    );
  // 完了の承認を選んだ課題の release は、その承認で同時に承認される（D12）ので独立に選べない。
  const coveredByCompletion = (o: Operation) =>
    o.kind === "release" &&
    waiting.some(
      (c) =>
        c.id === o.challenge_id &&
        c.status === "awaiting_completion_approval" &&
        isSel({ kind: "challenge", id: c.id }),
    );

  // 覆われた release は集合に入れない（表示では外れて見えるので、送る側も外す）。
  const effective = selected.filter((t) => {
    if (t.kind !== "operation") return true;
    const o = operations.find((x) => x.id === t.id);
    return !(o && coveredByCompletion(o));
  });

  const prepare = async (targets: ApprovalTarget[]) => {
    setBusy(true);
    setError(null);
    setDialogError(null);
    try {
      setPrepared(await api.prepareApproval(workspace, targets));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const execute = async () => {
    if (!prepared) return;
    setBusy(true);
    setDialogError(null);
    try {
      const r = await api.executeApproval(workspace, {
        challenge_id: prepared.challenge_id,
        items: prepared.items.map((i) => ({
          id: i.id,
          version: i.version,
          hold_release: i.hold_release,
        })),
        attestation: "mock-passkey",
      });
      setDone(
        `${r.approved.length} 件を承認しました: ${r.approved.join("、")}`,
      );
      setSelected([]);
      setPrepared(null);
    } catch (e) {
      if (isConflict(e)) {
        // 自動で再送しない。最新の要約を読み直して示し、もう一度確認を求める。
        try {
          setPrepared(
            await api.prepareApproval(
              workspace,
              prepared.items.map((i) => ({ kind: i.kind, id: i.id })),
            ),
          );
          setDialogError(
            `${errorText(e)} 最新の内容を読み直しました。もう一度確認してください。`,
          );
        } catch (e2) {
          setPrepared(null);
          setError(`${errorText(e)} / 読み直しにも失敗: ${errorText(e2)}`);
        }
      } else {
        setDialogError(errorText(e));
      }
    } finally {
      setBusy(false);
    }
  };

  const total = waiting.length + operations.length;
  return (
    <section className="write-form" aria-label="承認待ちの集合">
      <p className="form-meta">
        {workspace} の承認待ち {total} 件。選んだ分を 1
        回のパスキーでまとめて承認します。
      </p>
      {total === 0 ? <p>承認待ちはありません。</p> : null}
      <ul className="pick-list">
        {waiting.map((c) => {
          const t: ApprovalTarget = { kind: "challenge", id: c.id };
          return (
            <li key={c.id}>
              <label>
                <input
                  type="checkbox"
                  checked={isSel(t)}
                  onChange={() => toggle(t)}
                />{" "}
                <code>{c.id}</code>{" "}
                <span className="pick-kind">
                  {c.status === "awaiting_plan_approval" ? "計画" : "完了"}
                </span>{" "}
                <ClampedText text={c.title} />
              </label>
            </li>
          );
        })}
        {operations.map((o) => {
          const t: ApprovalTarget = { kind: "operation", id: o.id };
          const covered = coveredByCompletion(o);
          return (
            <li key={o.id}>
              <label>
                <input
                  type="checkbox"
                  checked={isSel(t) && !covered}
                  disabled={covered}
                  onChange={() => toggle(t)}
                />{" "}
                <code>{o.id}</code>{" "}
                <span className="pick-kind">不可逆操作 {o.kind}</span> （
                <code>{o.challenge_id}</code>）<ClampedText text={o.summary} />
                {covered ? (
                  <span className="pick-note">
                    {" "}
                    ＝ {o.challenge_id} の完了の承認で同時に承認されます
                  </span>
                ) : null}
              </label>
            </li>
          );
        })}
      </ul>
      {error ? (
        <p role="alert" className="form-error">
          {error}
        </p>
      ) : null}
      {done ? <output className="form-ok">{done}</output> : null}
      <button
        type="button"
        className="primary"
        disabled={busy || effective.length === 0}
        onClick={() => void prepare(effective)}
      >
        選んだ {effective.length} 件をまとめて承認…
      </button>

      {prepared ? (
        <PasskeyDialog
          title={`${prepared.items.length} 件をまとめて承認`}
          error={dialogError}
          busy={busy}
          approveLabel="パスキーで確認して承認する"
          onApprove={() => void execute()}
          onCancel={() => {
            setPrepared(null);
            setDialogError(null);
          }}
        >
          <p className="form-meta">
            対象はこの画面を出した時点で固定されています（後から増えた承認待ちは含みません）。署名の失効:{" "}
            {new Date(prepared.expires_at).toLocaleTimeString()}
          </p>
          <table className="approval-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>タイトル</th>
                <th>種類</th>
                <th>承認する版</th>
                <th>hold-release</th>
              </tr>
            </thead>
            <tbody>
              {prepared.items.map((i) => (
                <tr key={`${i.kind}:${i.id}`}>
                  <td>
                    <code>{i.id}</code>
                  </td>
                  <td>
                    <ClampedText text={i.title} />
                    {i.simultaneous_operation_ids.length > 0 ? (
                      <div className="pick-note">
                        同時に承認される:{" "}
                        {i.simultaneous_operation_ids.join("、")}
                      </div>
                    ) : null}
                  </td>
                  <td>{KIND_LABEL[i.approval_kind]}</td>
                  <td>v{i.version}</td>
                  <td>
                    {i.approval_kind === "completion"
                      ? i.hold_release
                        ? "あり"
                        : "なし"
                      : "-"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {prepared.items.map((i) => (
            <details key={`d-${i.kind}:${i.id}`}>
              <summary>{i.id} の要約を開く</summary>
              <ClampedText text={i.summary} />
              {i.pr_head ? (
                <p>
                  PR の最新のコミット（head）: <code>{i.pr_head}</code>
                </p>
              ) : null}
            </details>
          ))}
        </PasskeyDialog>
      ) : null}
    </section>
  );
}
