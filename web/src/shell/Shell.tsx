import type { ReactNode } from "react";
import { AllScreen } from "../all/AllScreen.tsx";
import { api } from "../api/client.ts";
import { CardDetail } from "../board/CardDetail.tsx";
import { toAgentBoard } from "../board/adapt.ts";
import { ChallengesScreen } from "../challenges/ChallengesScreen.tsx";
import { CycleScreen } from "../cycle/CycleScreen.tsx";
import { FollowupsScreen } from "../followups/FollowupsScreen.tsx";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { RefreshKeyContext } from "../nav.tsx";
import { NowScreen } from "../now/NowScreen.tsx";
import { PlanApprovalScreen } from "../plan-approval/PlanApprovalScreen.tsx";
import { AgentRail } from "./AgentRail.tsx";
import { NotFound } from "./NotFound.tsx";
import { TopBar } from "./TopBar.tsx";
import { type Route, hrefOf, useHashRoute } from "./route.ts";
import { useScreenData } from "./useScreenData.ts";

export const NO_AGENTS_TEXT = "エージェントがありません。";
export { LOADING_TEXT };

// 画面の枠組み: 左の列（エージェント）・上の帯・本文。右の対話の欄は作らない。
export function Shell({ live }: { live: LiveState }) {
  const [route, go] = useHashRoute();
  const agents = live.workspaces;
  const first = agents[0];

  // ハッシュが無いときは、一覧の先頭のエージェントの「いま進められること」。
  const current: Route | null =
    route ??
    (first ? { kind: "agent", name: first.name, screen: "now" } : null);
  const agentName =
    current && current.kind !== "all" && current.kind !== "unknown"
      ? current.name
      : null;
  const agent = agentName
    ? agents.find((a) => a.name === agentName)
    : undefined;

  let body: ReactNode;
  if (!live.listLoaded) {
    body = live.loadError ? (
      <p role="alert">読み込みに失敗: {live.loadError}</p>
    ) : (
      <p>{LOADING_TEXT}</p>
    );
  } else if (first === undefined) {
    body = <p>{NO_AGENTS_TEXT}</p>;
  } else if (current === null || current.kind === "unknown") {
    body = <NotFound homeHref={homeOf(first.name)} />;
  } else if (current.kind === "all") {
    body = <AllScreen live={live} />;
  } else if (agent === undefined) {
    body = <NotFound homeHref={homeOf(first.name)} />;
  } else if (agent.state !== "ok") {
    body = (
      <p role="alert" data-testid="agent-error">
        {agent.error ?? agent.state}
      </p>
    );
  } else {
    body = (
      <AgentBody live={live} route={current} agentName={agent.name} go={go} />
    );
  }

  return (
    <div className="shell">
      <AgentRail live={live} current={current} />
      <div className="shell-main">
        <TopBar live={live} current={current} agent={agent} />
        {live.listLoaded && live.loadError ? (
          <p role="alert">読み込みに失敗: {live.loadError}</p>
        ) : null}
        {live.fleetError ? (
          <p role="alert">集約の読み込みに失敗: {live.fleetError}</p>
        ) : null}
        <div className="shell-body">{body}</div>
      </div>
    </div>
  );
}

const homeOf = (name: string) => hrefOf({ kind: "agent", name, screen: "now" });

function AgentBody({
  live,
  route,
  agentName,
  go,
}: {
  live: LiveState;
  route: Exclude<Route, { kind: "all" } | { kind: "unknown" }>;
  agentName: string;
  go: (r: Route, opts?: { replace?: boolean }) => void;
}) {
  const data = live.data[agentName];

  switch (route.kind) {
    case "agent":
      switch (route.screen) {
        case "now":
          return (
            <NowScreen key={agentName} live={live} agentName={agentName} />
          );
        case "challenges":
          return <ChallengesScreen live={live} agentName={agentName} />;
        case "followups":
          return (
            <FollowupsScreen
              live={live}
              agentName={agentName}
              query={route.query}
              onNavigate={go}
            />
          );
        case "cycle":
          return <CycleScreen live={live} agentName={agentName} />;
      }
      return null;
    case "challenge":
      return <ChallengeBody live={live} agentName={agentName} id={route.id} />;
    case "approve_plan":
      return (
        <ChallengeGate live={live} agentName={agentName} id={route.id}>
          <PlanApprovalScreen
            key={`${agentName}/${route.id}`}
            live={live}
            agentName={agentName}
            id={route.id}
            workspacePath={
              live.workspaces.find((w) => w.name === agentName)?.path ?? ""
            }
          />
        </ChallengeGate>
      );
    case "task":
      return (
        <TaskBody
          live={live}
          agentName={agentName}
          id={route.id}
          taskId={route.taskId}
        />
      );
  }
}

// 課題が一覧に無いときは「見つかりません」。一覧を読み終えるまでは出さない。
function ChallengeGate({
  live,
  agentName,
  id,
  children,
}: {
  live: LiveState;
  agentName: string;
  id: string;
  children: ReactNode;
}) {
  const data = live.data[agentName];
  if (!data) return <p>{LOADING_TEXT}</p>;
  if (data.error) return <p role="alert">{data.error}</p>;
  if (!data.challenges?.challenges.some((c) => c.id === id)) {
    return <NotFound homeHref={homeOf(agentName)} />;
  }
  return <>{children}</>;
}

function ChallengeBody({
  live,
  agentName,
  id,
}: {
  live: LiveState;
  agentName: string;
  id: string;
}) {
  const agent = live.workspaces.find((w) => w.name === agentName);
  const data = live.data[agentName];
  const challenge =
    agent && data
      ? toAgentBoard(agent, data).challenges.find((c) => c.id === id)
      : undefined;
  return (
    <ChallengeGate live={live} agentName={agentName} id={id}>
      {agent && challenge ? (
        <RefreshKeyContext.Provider value={live.lastUpdated}>
          <CardDetail
            challenge={challenge}
            agentName={agentName}
            workspacePath={agent.path}
          />
        </RefreshKeyContext.Provider>
      ) : null}
    </ChallengeGate>
  );
}

function TaskBody({
  live,
  agentName,
  id,
  taskId,
}: {
  live: LiveState;
  agentName: string;
  id: string;
  taskId: string;
}) {
  const { result } = useScreenData(live, agentName, () => api.tree(agentName));
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error")
    return <p role="alert">{String(result.error)}</p>;
  const tree = result.data;
  const found = [...tree.challenges, ...tree.recent_done]
    .find((c) => c.id === id)
    ?.tasks.find((t) => t.id === taskId);
  if (!found) return <NotFound homeHref={homeOf(agentName)} />;
  return (
    <section aria-labelledby="screen-title">
      <h2 id="screen-title">タスクの詳細</h2>
    </section>
  );
}
