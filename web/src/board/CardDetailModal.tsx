import { useContext, useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import type {
  Activity,
  RunReport,
  ShowCardResponse,
} from "../api/types.gen.ts";
import { STATE_LABELS, label } from "../labels.ts";
import { RefreshKeyContext } from "../nav.tsx";
import { ApprovalGuide } from "./ApprovalGuide.tsx";
import { ApprovalPanel, approvalKindOf } from "./ApprovalPanel.tsx";
import { CardFields } from "./CardFields.tsx";
import { ClampedText } from "./ClampedText.tsx";
import { StagesDetail } from "./StagesDetail.tsx";
import type { Challenge } from "./types.ts";

export interface Loaded {
  show: ShowCardResponse;
  activities: Activity[];
  reports: Record<string, RunReport>;
}

const byTimeAsc = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

export async function load(workspace: string, id: string): Promise<Loaded> {
  const [show, log] = await Promise.all([
    api.challenge(workspace, id),
    api.log(workspace, id),
  ]);
  const reports: Record<string, RunReport> = {};
  await Promise.all(
    show.runs.map(async (r) => {
      try {
        reports[r.id] = (await api.runReport(workspace, r.id)).report;
      } catch {
        // report の無い run は、assumptions と unverified を出さないだけにする
      }
    }),
  );
  return { show, activities: log.activities, reports };
}

function List({ items }: { items: string[] }) {
  return (
    <ul>
      {items.map((x, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 同じ文言が並びうる固定の一覧
        <li key={i}>
          <ClampedText text={x} />
        </li>
      ))}
    </ul>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h3>
        {title}（{count}）
      </h3>
      {count === 0 ? <p>なし</p> : children}
    </section>
  );
}

// 保留の原因の run が無い（run_id が null）ときの表示。
const CAUSE_RUN_NONE = "なし（原因の run は記録されていない）";

type CardDetailModalProps = {
  challenge: Challenge;
  agentName: string;
  workspacePath: string;
  onClose: () => void;
  // 開発時の mock の案の画面（関係グラフ）だけが渡す差し込み口。「課題」の節の後ろに描く。
  // ボードは渡さないので、省略時は何も描かない（ボードの表示は変わらない）。
  supplement?: React.ReactNode;
};

type LoadState =
  | { status: "loading" }
  | { status: "success"; loaded: Loaded }
  | { status: "error" };

export function CardDetailModal({
  challenge,
  agentName,
  workspacePath,
  onClose,
  supplement,
}: CardDetailModalProps) {
  const [loadState, setLoadState] = useState<LoadState>({ status: "loading" });
  const refreshKey = useContext(RefreshKeyContext);
  const [localReload, setLocalReload] = useState(0);
  const approvalKind = approvalKindOf(challenge.nextHumanActions);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const mouseDownOnDialog = useRef(false);
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  // <dialog> の showModal() で開く（Esc・フォーカストラップ・背景の不活性化はブラウザの標準動作に任せる）。
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }
    if (!dialog.open && typeof dialog.showModal === "function") {
      dialog.showModal();
    }
    const handleClose = () => onCloseRef.current();
    dialog.addEventListener("close", handleClose);
    return () => dialog.removeEventListener("close", handleClose);
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    let cancelled = false;
    load(agentName, challenge.id).then(
      (loaded) => {
        if (!cancelled) setLoadState({ status: "success", loaded });
      },
      () => {
        if (!cancelled) setLoadState({ status: "error" });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [agentName, challenge.id, refreshKey, localReload]);

  const close = () => {
    const dialog = dialogRef.current;
    if (dialog?.open) {
      dialog.close();
    } else {
      onCloseRef.current();
    }
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: 背景クリックで閉じるのは補助。キーボードの Esc は <dialog> が標準で閉じる
    <dialog
      ref={dialogRef}
      className="card-detail-modal"
      data-testid="modal-overlay"
      aria-labelledby="card-detail-modal-title"
      onMouseDown={(event) => {
        mouseDownOnDialog.current = event.target === event.currentTarget;
      }}
      onClick={(event) => {
        if (mouseDownOnDialog.current && event.target === event.currentTarget) {
          close();
        }
        mouseDownOnDialog.current = false;
      }}
    >
      <div
        className="card-detail-modal-content"
        data-testid="card-detail-content"
      >
        <div className="card-detail-modal-header">
          <h2 id="card-detail-modal-title" className="card-detail-modal-title">
            {challenge.headline ?? challenge.title}
          </h2>
          <button type="button" className="modal-close-button" onClick={close}>
            閉じる
          </button>
        </div>

        <CardFields
          challenge={challenge}
          className="card-detail-card-fields"
          testId="card-detail-card-fields"
        />

        <dl className="card-detail-fields">
          <dt>ID</dt>
          <dd>{challenge.id}</dd>
          <dt>タイトル</dt>
          <dd>{challenge.title}</dd>
          <dt>ステータス</dt>
          <dd>{challenge.status}</dd>
          <dt>優先度</dt>
          <dd>{challenge.priority ?? "-"}</dd>
          <dt>要対応</dt>
          <dd>{challenge.needsHuman ? "はい" : "いいえ"}</dd>
          <dt>見出し</dt>
          <dd data-testid="detail-headline">
            {challenge.headline ?? challenge.title}
          </dd>
        </dl>

        <section className="card-detail-ledger-join" data-testid="ledger-join">
          <h3 className="card-detail-ledger-join-heading">課題</h3>
          <dl className="card-detail-fields">
            <dt>完了条件</dt>
            <dd
              className="card-detail-multiline"
              data-testid="ledger-completion-criteria"
            >
              {challenge.completionCriteria ?? "-"}
            </dd>
            <dt>説明</dt>
            <dd
              className="card-detail-multiline card-detail-description"
              data-testid="ledger-description"
            >
              {challenge.description ?? "-"}
            </dd>
          </dl>
        </section>

        {supplement}

        {loadState.status === "loading" && (
          <div className="card-detail-log-loading">読み込み中...</div>
        )}
        {loadState.status === "error" && (
          <div className="card-detail-log-error">詳細の取得に失敗しました</div>
        )}
        {loadState.status === "success" && (
          <DetailSections l={loadState.loaded} />
        )}

        {approvalKind && (
          <ApprovalPanel
            key={approvalKind}
            workspace={agentName}
            challengeId={challenge.id}
            kind={approvalKind}
            onApproved={() => setLocalReload((n) => n + 1)}
          />
        )}

        <ApprovalGuide
          challenge={challenge}
          workspacePath={workspacePath}
          className="card-detail-modal-footer"
        />
      </div>
    </dialog>
  );
}

// 計画・承認の記録・保留・不可逆操作・run・作業ログ。カードの詳細のモーダルに出す。
export function DetailSections({ l }: { l: Loaded }) {
  const { show, activities, reports } = l;
  const plans = [...show.plans].sort((a, b) => a.version - b.version);
  const runs = [...show.runs].sort((a, b) =>
    byTimeAsc(b.started_at, a.started_at),
  );
  const acts = [...activities].sort((a, b) => byTimeAsc(a.at, b.at));
  return (
    <>
      <StagesDetail stages={show.stages} />
      <Section title="計画" count={plans.length}>
        {plans.map((p) => (
          <article key={p.version} data-plan={p.version}>
            <h4>
              v{p.version}（{p.created_at}）
            </h4>
            <pre style={{ whiteSpace: "pre-wrap" }}>{p.body}</pre>
          </article>
        ))}
      </Section>
      <Section title="承認と差し戻しの記録" count={show.approvals.length}>
        <ul>
          {show.approvals.map((a) => (
            <li
              key={`${a.kind}/${a.target_version}/${a.decided_at}/${a.operation_id ?? ""}`}
            >
              {a.decided_at} {a.kind} v{a.target_version}: {a.decision}（
              {a.actor}）
              {a.reason ? (
                <>
                  {" "}
                  <ClampedText text={a.reason} />
                </>
              ) : null}
            </li>
          ))}
        </ul>
      </Section>
      <Section title="保留の問いと回答" count={show.holds.length}>
        <ul>
          {show.holds.map((h) => (
            <li
              key={h.raised_at}
              data-hold={h.answer === null ? "open" : "answered"}
            >
              <div>
                問い（{h.raised_at}）: <ClampedText text={h.question} />
              </div>
              <div>
                回答:{" "}
                {h.answer === null ? "未回答" : <ClampedText text={h.answer} />}
              </div>
              {h.answer === null ? (
                <>
                  <div>保留に入った日時: {h.raised_at}</div>
                  <div>
                    入る直前の状態:{" "}
                    {h.from_status_label || label(STATE_LABELS, h.from_status)}
                  </div>
                  <div>原因の run: {h.run_id ?? CAUSE_RUN_NONE}</div>
                </>
              ) : null}
            </li>
          ))}
        </ul>
      </Section>
      <Section title="不可逆操作" count={show.operations.length}>
        <ul>
          {show.operations.map((o) => (
            <li key={o.id}>
              {o.kind} / {o.state}: <ClampedText text={o.summary} />
            </li>
          ))}
        </ul>
      </Section>
      <Section title="run" count={runs.length}>
        <ul>
          {runs.map((r) => {
            const rep = reports[r.id];
            return (
              <li key={r.id} data-run={r.id}>
                <code>{r.id}</code> {r.kind} ・ {r.started_at}
                {r.result ? ` ・ ${r.result}` : ""}
                {rep && rep.assumptions.length > 0 ? (
                  <div>
                    仮定:
                    <List items={rep.assumptions} />
                  </div>
                ) : null}
                {rep && rep.unverified.length > 0 ? (
                  <div>
                    未検証:
                    <List items={rep.unverified} />
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      </Section>
      <Section title="作業ログ" count={acts.length}>
        <ol>
          {acts.map((a) => (
            <li key={`${a.at}/${a.entity}/${a.entity_id}/${a.action}`}>
              {a.at} {a.actor} {a.action} {a.entity} {a.entity_id}
            </li>
          ))}
        </ol>
      </Section>
    </>
  );
}
