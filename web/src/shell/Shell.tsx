import { type ReactNode, useCallback, useMemo, useState } from "react";
import { AllScreen } from "../all/AllScreen.tsx";
import { ApproveCompletionScreen } from "../approve-completion/ApproveCompletionScreen.tsx";
import { ChallengesScreen } from "../challenges/ChallengesScreen.tsx";
import { ChatPanel, type Viewing } from "../chat/ChatPanel.tsx";
import { ChatContext } from "../chat/context.ts";
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

// 画面の枠組み: 左の列（エージェント）・上の帯・本文・右の対話の欄。
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

  // 対話の欄。たたんだ状態は画面を移っても保つ（枠が持つ）。相手は選んだエージェント。
  // エージェントを選んでいない画面（すべて・見つかりません）では、一覧の先頭の読めるエージェントにする。
  const [chatOpen, setChatOpen] = useState(true);
  const [focusRequest, setFocusRequest] = useState(0);
  const [taskTitle, setTaskTitle] = useState<{
    taskId: string;
    title: string;
  } | null>(null);
  // エージェントを選んでいる画面では、そのエージェントだけが相手（読めない・一覧に無いときは欄を出さない）。
  // 代わりのエージェントへ黙って回すと、見ている画面と違う相手へ発言が届くため。
  const chatAgent =
    agentName === null
      ? agents.find((a) => a.state === "ok")
      : agent && agent.state === "ok"
        ? agent
        : undefined;
  const openAndFocus = useCallback(() => {
    setChatOpen(true);
    setFocusRequest((n) => n + 1);
  }, []);
  const reportTask = useCallback(
    (taskId: string | null, title: string | null) =>
      setTaskTitle(
        taskId !== null && title !== null ? { taskId, title } : null,
      ),
    [],
  );
  const chatContext = useMemo(
    () => ({ openAndFocus, reportTask }),
    [openAndFocus, reportTask],
  );
  const challengeTitle = (id: string): string | null =>
    live.data[agentName ?? ""]?.challenges?.challenges.find((c) => c.id === id)
      ?.title ?? null;
  let viewing: Viewing | null = null;
  if (current && agent && agent.state === "ok") {
    if (
      current.kind === "challenge" ||
      current.kind === "approve_plan" ||
      current.kind === "approve_completion"
    ) {
      viewing = {
        challengeId: current.id,
        taskId: null,
        title: challengeTitle(current.id),
      };
    } else if (current.kind === "task") {
      viewing = {
        challengeId: current.id,
        taskId: current.taskId,
        title: taskTitle?.taskId === current.taskId ? taskTitle.title : null,
      };
    }
  }

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
    <ChatContext.Provider value={chatContext}>
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
        {chatAgent ? (
          <ChatPanel
            key={chatAgent.name}
            live={live}
            agentName={chatAgent.name}
            open={chatOpen}
            onToggle={() => setChatOpen((o) => !o)}
            viewing={viewing}
            focusRequest={focusRequest}
          />
        ) : null}
      </div>
    </ChatContext.Provider>
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
      // 画面が自分の状態（ボタンの結果・開いた状態）を持つものは、エージェントごとに作り直す（key）。
      switch (route.screen) {
        case "now":
          return (
            <NowScreen key={agentName} live={live} agentName={agentName} />
          );
        case "challenges":
          return (
            <ChallengesScreen
              key={agentName}
              live={live}
              agentName={agentName}
            />
          );
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
          return (
            <CycleScreen key={agentName} live={live} agentName={agentName} />
          );
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
    case "approve_completion":
      return (
        <ChallengeGate live={live} agentName={agentName} id={route.id}>
          <ApproveCompletionScreen
            key={`${agentName}/${route.id}`}
            live={live}
            agentName={agentName}
            id={route.id}
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
