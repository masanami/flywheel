import type { ReactNode } from "react";
import { AllScreen } from "../all/AllScreen.tsx";
import { ChallengesScreen } from "../challenges/ChallengesScreen.tsx";
import { CycleScreen } from "../cycle/CycleScreen.tsx";
import { ChallengeDetail } from "../detail/ChallengeDetail.tsx";
import { FollowupsScreen } from "../followups/FollowupsScreen.tsx";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { NowScreen } from "../now/NowScreen.tsx";
import { PlanApprovalScreen } from "../plan-approval/PlanApprovalScreen.tsx";
import { TaskDetail } from "../task/TaskDetail.tsx";
import { AgentRail } from "./AgentRail.tsx";
import { NotFound } from "./NotFound.tsx";
import { TopBar } from "./TopBar.tsx";
import { type Route, hrefOf, useHashRoute } from "./route.ts";

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
      return (
        <ChallengeDetail
          live={live}
          agentName={agentName}
          workspacePath={
            live.workspaces.find((w) => w.name === agentName)?.path ?? ""
          }
          id={route.id}
          view={route.view}
        />
      );
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
        <TaskDetail
          live={live}
          agentName={agentName}
          workspacePath={
            live.workspaces.find((w) => w.name === agentName)?.path ?? ""
          }
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
