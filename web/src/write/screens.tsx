import { useEffect, useState } from "react";
import { api } from "../api/client.ts";
import type { ChallengeCard, StatusNeedsHuman } from "../api/types.gen.ts";
import type { CardExtra } from "../api/write.ts";
import { Board } from "../board/Board.tsx";
import { CardDetailModal } from "../board/CardDetailModal.tsx";
import { toChallenge } from "../board/adapt.ts";
import type { LiveState } from "../live/useLive.ts";
import { RefreshKeyContext } from "../nav.tsx";
import type { Screen } from "../screens.tsx";
import { ApproveBatch } from "./ApproveBatch.tsx";
import { CreateForm } from "./CreateForm.tsx";
import { CycleHeaders } from "./CycleHeaders.tsx";
import { DecisionPanel } from "./DecisionPanel.tsx";
import { EditForm } from "./EditForm.tsx";
import { NeedsHumanStrip } from "./NeedsHumanStrip.tsx";
import { ProposalCard, pointsOf } from "./ProposalCard.tsx";
import { WorkspaceSelect, useWorkspaceChoice } from "./common.tsx";

function NoWorkspace() {
  return <p>読み込めるワークスペースがありません。</p>;
}

function cardsOf(live: LiveState, ws: string): ChallengeCard[] {
  return live.data[ws]?.challenges?.challenges ?? [];
}

// --- S2: 起票 ---
function CreateScreen({ live }: { live: LiveState }) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  if (!current) return <NoWorkspace />;
  return (
    <section aria-label="起票">
      <h2>起票</h2>
      <WorkspaceSelect
        workspaces={ok}
        value={current.name}
        onChange={setPicked}
      />
      <CreateForm key={current.name} workspace={current.name} />
    </section>
  );
}

// --- S2: 編集・優先度 ---
function EditScreen({ live }: { live: LiveState }) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  // 開いた時点の課題（版を含む）を控える。以後に更新されても、この版を添えて送る。
  const [opened, setOpened] = useState<ChallengeCard | null>(null);
  if (!current) return <NoWorkspace />;
  const cards = cardsOf(live, current.name);
  return (
    <section aria-label="編集と優先度">
      <h2>編集と優先度</h2>
      <WorkspaceSelect
        workspaces={ok}
        value={current.name}
        onChange={(n) => {
          setPicked(n);
          setOpened(null);
        }}
      />
      <ul className="pick-list">
        {cards.map((c) => (
          <li key={c.id}>
            <button
              type="button"
              aria-pressed={opened?.id === c.id}
              onClick={() => setOpened(c)}
            >
              <code>{c.id}</code> {c.title.slice(0, 40)}（版 {c.version}）
            </button>
          </li>
        ))}
      </ul>
      {opened ? (
        <EditForm
          key={`${current.name}/${opened.id}/${opened.version}`}
          workspace={current.name}
          base={opened}
          hasUpstream={opened.id === "C-10"}
        />
      ) : (
        <p>編集する課題を選んでください。</p>
      )}
    </section>
  );
}

// --- S3: まとめての承認 ---
function ApproveScreen({ live }: { live: LiveState }) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  if (!current) return <NoWorkspace />;
  const nh: StatusNeedsHuman | undefined = live.needsHuman?.workspaces.find(
    (w) => w.name === current.name,
  )?.needs_human;
  return (
    <section aria-label="まとめて承認">
      <h2>まとめて承認</h2>
      <WorkspaceSelect
        workspaces={ok}
        value={current.name}
        onChange={setPicked}
      />
      <ApproveBatch
        key={current.name}
        workspace={current.name}
        cards={cardsOf(live, current.name)}
        operations={nh?.operations ?? []}
      />
    </section>
  );
}

// --- S3: 差し戻し・回答・予算の増額 ---
function DecisionScreen({ live }: { live: LiveState }) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  const [message, setMessage] = useState<string | null>(null);
  if (!current) return <NoWorkspace />;
  const nh = live.needsHuman?.workspaces.find(
    (w) => w.name === current.name,
  )?.needs_human;
  const holds = Object.fromEntries(
    (live.questions?.questions ?? [])
      .filter((q) => q.workspace === current.name)
      .map((q) => [q.challenge_id, { question: q.question }]),
  );
  return (
    <section aria-label="差し戻し・回答・増額">
      <h2>差し戻し・回答・増額</h2>
      <p className="form-meta">
        1 件ずつ、操作のたびにパスキーを求めます（まとめられるのは承認だけ）。
      </p>
      <WorkspaceSelect
        workspaces={ok}
        value={current.name}
        onChange={setPicked}
      />
      {message ? <output className="form-ok">{message}</output> : null}
      <DecisionPanel
        key={current.name}
        workspace={current.name}
        cards={cardsOf(live, current.name)}
        needsHuman={nh}
        holds={holds}
        onDone={setMessage}
      />
    </section>
  );
}

// --- S4: 課題カード 1 枚 ---
function CardScreen({ live }: { live: LiveState }) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  const [pickedId, setPickedId] = useState<string | null>(null);
  const [narrow, setNarrow] = useState(true);
  const [open, setOpen] = useState(false);
  const [extra, setExtra] = useState<CardExtra | null>(null);
  const cards = current ? cardsOf(live, current.name) : [];
  const card = cards.find((c) => c.id === pickedId) ?? cards[0];
  const wsName = current?.name;
  const cardId = card?.id;
  // 別のカードへ替えたら、前のカードの項目を残さない
  // biome-ignore lint/correctness/useExhaustiveDependencies: カードの切り替えが合図
  useEffect(() => setExtra(null), [wsName, cardId]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: lastUpdated は再取得の合図
  useEffect(() => {
    if (!wsName || !cardId) return;
    let alive = true;
    api.cardExtra(wsName, cardId).then(
      (e) => alive && setExtra(e),
      () => alive && setExtra(null),
    );
    return () => {
      alive = false;
    };
  }, [wsName, cardId, live.lastUpdated]);

  if (!current) return <NoWorkspace />;
  const nh = live.needsHuman?.workspaces.find(
    (w) => w.name === current.name,
  )?.needs_human;
  const question = live.questions?.questions.find(
    (q) => q.workspace === current.name && q.challenge_id === card?.id,
  )?.question;
  return (
    <section aria-label="課題カード">
      <h2>課題カード（1 枚）</h2>
      <div className="inline-row">
        <WorkspaceSelect
          workspaces={ok}
          value={current.name}
          onChange={(n) => {
            setPicked(n);
            setPickedId(null);
          }}
        />
        <label className="field-inline">
          課題
          <select
            value={card?.id ?? ""}
            onChange={(e) => setPickedId(e.target.value)}
          >
            {cards.map((c) => (
              <option key={c.id} value={c.id}>
                {c.id} {c.title.slice(0, 30)}
              </option>
            ))}
          </select>
        </label>
        <label className="field-inline">
          <input
            type="checkbox"
            checked={narrow}
            onChange={(e) => setNarrow(e.target.checked)}
          />
          列の幅（320px）で見る
        </label>
      </div>
      {card ? (
        <div className={narrow ? "proposal-frame narrow" : "proposal-frame"}>
          <ProposalCard
            card={card}
            extra={extra}
            points={pointsOf(card, nh, question)}
            onOpen={() => setOpen(true)}
          />
        </div>
      ) : (
        <p>課題がありません。</p>
      )}
      {open && card ? (
        <RefreshKeyContext.Provider value={live.lastUpdated}>
          <CardDetailModal
            challenge={toChallenge(card)}
            agentName={current.name}
            workspacePath={current.path}
            onClose={() => setOpen(false)}
          />
        </RefreshKeyContext.Provider>
      ) : null}
    </section>
  );
}

// --- UX 案①③: 人間の操作待ちの帯と列の見出しの案つきのボード ---
function BoardProposalScreen({ live }: { live: LiveState }) {
  return (
    <section aria-label="ボード（案）" className="board-screen">
      <NeedsHumanStrip live={live} />
      <CycleHeaders
        workspaces={live.workspaces}
        refreshKey={live.lastUpdated}
      />
      <Board live={live} />
    </section>
  );
}

// 開発時の mock モードで足す画面。部品は本物で、データの出どころ（api）だけが偽の API。
export const writeScreens: Screen[] = [
  {
    id: "board-proposal",
    title: "ボード（案）",
    render: (live) => <BoardProposalScreen live={live} />,
  },
  {
    id: "create",
    title: "起票",
    render: (live) => <CreateScreen live={live} />,
  },
  {
    id: "edit",
    title: "編集と優先度",
    render: (live) => <EditScreen live={live} />,
  },
  {
    id: "approve",
    title: "まとめて承認",
    render: (live) => <ApproveScreen live={live} />,
  },
  {
    id: "decide",
    title: "差し戻し・回答・増額",
    render: (live) => <DecisionScreen live={live} />,
  },
  {
    id: "card",
    title: "課題カード",
    render: (live) => <CardScreen live={live} />,
  },
];
