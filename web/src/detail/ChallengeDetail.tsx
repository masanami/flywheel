import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import { useChat } from "../chat/context.ts";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { ConflictFixesView } from "../parts/ConflictFixesView.tsx";
import { NotFound } from "../shell/NotFound.tsx";
import { CHALLENGE_VIEWS, type ChallengeView, hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { History } from "./History.tsx";
import { Items } from "./Items.tsx";
import { PlanView } from "./PlanView.tsx";
import { TreeView } from "./TreeView.tsx";
import { Waiting } from "./Waiting.tsx";

const VIEW_LABELS: Record<ChallengeView, string> = {
  tree: "ツリー",
  history: "経緯",
  plan: "計画の本文",
};

// 課題の詳細の画面。上から、題名 → 課題の項目 → あなたを待っているもの → 切り替え → 選んだ中身。
// 課題の詳細の API は、この画面を開いている間だけ読み、そのエージェントの変化の通し番号が進んだら読み直す。
// 応答が返るまでは「見つかりません」を出さない（not_found の応答だけが見つかりませんになる）。
export function ChallengeDetail({
  live,
  agentName,
  workspacePath,
  id,
  view,
}: {
  live: LiveState;
  agentName: string;
  workspacePath: string;
  id: string;
  view: ChallengeView;
}) {
  const chat = useChat();
  const { result } = useScreenData(
    live,
    agentName,
    () => api.challenge(agentName, id),
    [id],
  );
  const home = hrefOf({ kind: "agent", name: agentName, screen: "now" });
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error") {
    const e = result.error;
    if (e instanceof ApiError && e.code === "not_found") {
      return <NotFound homeHref={home} />;
    }
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const show = result.data;
  const titleOf = (cid: string) =>
    live.data[agentName]?.challenges?.challenges.find((c) => c.id === cid)
      ?.title;
  return (
    <article
      className="detail"
      data-testid="challenge-detail"
      aria-labelledby="detail-title"
    >
      <h2 id="detail-title" data-testid="detail-title">
        {show.challenge.title}
      </h2>
      <p>
        <button type="button" onClick={chat.openAndFocus}>
          この課題について相談する
        </button>
      </p>
      <Items show={show} />
      <Waiting
        show={show}
        agentName={agentName}
        workspacePath={workspacePath}
      />
      <ConflictFixesView
        fixes={show.conflict_fixes}
        titleOfTask={(tid) => show.tasks.find((t) => t.id === tid)?.title}
        showAt
      />
      <nav aria-label="課題の詳細の切り替え" className="detail-switch">
        {CHALLENGE_VIEWS.map((v) => (
          <a
            key={v}
            href={hrefOf({ kind: "challenge", name: agentName, id, view: v })}
            aria-current={v === view ? "page" : undefined}
          >
            {VIEW_LABELS[v]}
          </a>
        ))}
      </nav>
      {view === "tree" ? (
        <TreeView show={show} agentName={agentName} titleOf={titleOf} />
      ) : null}
      {view === "plan" ? <PlanView key={id} show={show} /> : null}
      {view === "history" ? (
        <History key={id} live={live} agentName={agentName} show={show} />
      ) : null}
    </article>
  );
}
