import { type ReactNode, useContext } from "react";
import type {
  Challenge,
  ChallengeCard,
  StatusNeedsHuman,
  Workspace,
} from "./api/types.gen.ts";
import { CardById, ClampedText, cardIndex } from "./cards.tsx";
import { DISCREPANCY_LABELS, STATE_LABELS, label } from "./labels.ts";
import type { LiveState } from "./live/useLive.ts";
import { OpenDetailContext, WorkspaceNameContext } from "./nav.tsx";

export interface Screen {
  id: string;
  title: string;
  render: (live: LiveState) => ReactNode;
}

const HUMAN_GROUPS = (
  [
    "awaiting_plan_approval",
    "awaiting_completion_approval",
    "awaiting_human",
  ] as const
).map((status) => ({ status, title: STATE_LABELS[status] }));

function WorkspaceError({ w }: { w: Workspace }) {
  return (
    <p role="alert" data-ws-state={w.state}>
      <ClampedText
        text={`${w.name}: ${w.state}${w.error ? `（${w.error}）` : ""}`}
      />
    </p>
  );
}

function Group({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <div>
      <h4>
        {title}（{count}）
      </h4>
      {count === 0 ? <p>なし</p> : <ul>{children}</ul>}
    </div>
  );
}

function NeedsHumanBody({
  nh,
  index,
}: {
  nh: StatusNeedsHuman;
  index: Map<string, ChallengeCard>;
}) {
  const byStatus = (s: string): Challenge[] =>
    nh.challenges.filter((c) => c.status === s);
  const known = new Set<string>(HUMAN_GROUPS.map((g) => g.status));
  const others = nh.challenges.filter((c) => !known.has(c.status));
  return (
    <>
      {HUMAN_GROUPS.map((g) => {
        const list = byStatus(g.status);
        return (
          <Group key={g.status} title={g.title} count={list.length}>
            {list.map((c) => (
              <CardById key={c.id} id={c.id} fallback={c} index={index} />
            ))}
          </Group>
        );
      })}
      {others.length > 0 ? (
        <Group title="その他" count={others.length}>
          {others.map((c) => (
            <CardById key={c.id} id={c.id} fallback={c} index={index} />
          ))}
        </Group>
      ) : null}
      <Group title="承認待ちの不可逆操作" count={nh.operations.length}>
        {nh.operations.map((o) => (
          <li key={o.id}>
            <code>{o.challenge_id}</code> {o.kind} / {o.state}:{" "}
            <ClampedText text={o.summary} />
          </li>
        ))}
      </Group>
      <Group title="上流との食い違い" count={nh.discrepancies.length}>
        {nh.discrepancies.map((d) => (
          <li key={d.challenge_id}>
            <code>{d.challenge_id}</code>:{" "}
            {d.kinds.map((k) => label(DISCREPANCY_LABELS, k)).join("、")}
          </li>
        ))}
      </Group>
      <Group title="トリアージ待ち" count={nh.triage.length}>
        {nh.triage.map((t) => (
          <li key={`${t.challenge_id}/${t.run_id}`}>
            <code>{t.challenge_id}</code> (run {t.run_id}):{" "}
            <ClampedText text={t.reason} />
          </li>
        ))}
      </Group>
      <Group title="予算の使い切り" count={nh.budget_exhausted.length}>
        {nh.budget_exhausted.map((b) => (
          <li key={`${b.challenge_id}/${b.plan_version}`}>
            <code>{b.challenge_id}</code>（計画 v{b.plan_version}）実装の残り $
            {b.impl_remaining_usd.toFixed(2)} / レビューの残り $
            {b.review_remaining_usd.toFixed(2)}
          </li>
        ))}
      </Group>
      <Group title="片づけ待ちのスロット" count={nh.slots.length}>
        {nh.slots.map((s) => (
          <li key={s.slot_id}>
            {s.repo} / <code>{s.slot_id}</code>
            {s.run_id ? ` (run ${s.run_id})` : ""}:{" "}
            <ClampedText text={s.path} />
          </li>
        ))}
      </Group>
    </>
  );
}

// 最上位: ワークスペースごとに、人間の操作を待っているものを集める。
function Top({ live }: { live: LiveState }) {
  const nhBy = new Map(
    (live.needsHuman?.workspaces ?? []).map((w) => [w.name, w.needs_human]),
  );
  return (
    <section aria-label="人間の対応待ち">
      <h2>人間の対応待ち</h2>
      {live.workspaces.map((w) => {
        const nh = nhBy.get(w.name);
        return (
          <section key={w.name} aria-label={`${w.name} の対応待ち`}>
            <WorkspaceNameContext.Provider value={w.name}>
              <h3>{w.name}</h3>
              {w.state !== "ok" ? (
                <WorkspaceError w={w} />
              ) : nh ? (
                <NeedsHumanBody
                  nh={nh}
                  index={cardIndex(live.data[w.name]?.challenges?.challenges)}
                />
              ) : live.fleetError ? (
                <p role="alert">取得に失敗: {live.fleetError}</p>
              ) : (
                <p>読み込み中</p>
              )}
            </WorkspaceNameContext.Provider>
          </section>
        );
      })}
      <h3>未回答の問い</h3>
      {(live.questions?.questions.length ?? 0) === 0 ? (
        <p>なし</p>
      ) : (
        <ul>
          {live.questions?.questions.map((q) => (
            <li key={`${q.workspace}/${q.challenge_id}/${q.raised_at}`}>
              {q.workspace} / <code>{q.challenge_id}</code>:{" "}
              <ClampedText text={q.question} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ワークスペースの画面: 人間の操作待ちとは別に、システムが進めるもの・外部待ち・承認済みの操作。
function WorkspaceBody({ w, live }: { w: Workspace; live: LiveState }) {
  const d = live.data[w.name];
  if (d?.error) return <p role="alert">取得に失敗: {d.error}</p>;
  if (!d?.status) return <p>読み込み中</p>;
  const index = cardIndex(d.challenges?.challenges);
  const { actionable, waiting_external, approved } = d.status;
  return (
    <>
      <Group
        title="システムが次に進められるもの"
        count={actionable.challenges.length}
      >
        {actionable.challenges.map((c) => (
          <CardById key={c.id} id={c.id} fallback={c} index={index} />
        ))}
      </Group>
      <Group
        title="外部を待っているもの"
        count={waiting_external.challenges.length}
      >
        {waiting_external.challenges.map((e) => (
          <li key={e.challenge_id}>
            <CardById id={e.challenge_id} index={index}>
              <div>
                チェック: <ClampedText text={e.checks} /> / PR:{" "}
                <ClampedText text={e.pr_url} />
              </div>
            </CardById>
          </li>
        ))}
      </Group>
      <Group title="承認済みの不可逆操作" count={approved.operations.length}>
        {approved.operations.map((o) => (
          <li key={o.id}>
            <code>{o.challenge_id}</code> {o.kind} / {o.state}:{" "}
            <ClampedText text={o.summary} />
          </li>
        ))}
      </Group>
    </>
  );
}

function Workspaces({ live }: { live: LiveState }) {
  return (
    <section aria-label="ワークスペース">
      <h2>ワークスペース</h2>
      {live.workspaces.map((w) => (
        <section key={w.name} aria-label={`${w.name} の状況`}>
          <WorkspaceNameContext.Provider value={w.name}>
            <h3>{w.name}</h3>
            {w.state !== "ok" ? (
              <WorkspaceError w={w} />
            ) : (
              <WorkspaceBody w={w} live={live} />
            )}
          </WorkspaceNameContext.Provider>
        </section>
      ))}
    </section>
  );
}

// 問いの一覧: GET /api/v1/questions の要素を返ってきた順に出し、各要素から課題の詳細を開く。
function Questions({ live }: { live: LiveState }) {
  const open = useContext(OpenDetailContext);
  const qs = live.questions?.questions ?? [];
  return (
    <section aria-label="問いの一覧">
      <h2>問いの一覧</h2>
      {qs.length === 0 ? (
        <p>なし</p>
      ) : (
        <ol>
          {qs.map((q) => (
            <li
              key={`${q.workspace}/${q.challenge_id}/${q.raised_at}`}
              data-testid="question"
            >
              <div>
                {q.workspace} / <code>{q.challenge_id}</code>:{" "}
                <ClampedText text={q.title} />
              </div>
              <div>
                <ClampedText text={q.question} />
              </div>
              <button
                type="button"
                onClick={() => open?.(q.workspace, q.challenge_id)}
              >
                課題の詳細を開く
              </button>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

// 画面の一覧。後続のチケットは画面をここへ足す。AC-123・AC-124 のテストはこの一覧を順に検査する。
export const screens: Screen[] = [
  {
    id: "needs-human",
    title: "人間の対応待ち",
    render: (live) => <Top live={live} />,
  },
  {
    id: "workspaces",
    title: "ワークスペース",
    render: (live) => <Workspaces live={live} />,
  },
  {
    id: "questions",
    title: "問いの一覧",
    render: (live) => <Questions live={live} />,
  },
];
