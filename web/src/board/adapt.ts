import type { ChallengeCard, Workspace } from "../api/types.gen.ts";
import {
  ACTION_LABELS,
  MODIFIER_LABELS,
  NO_ACTION_LABEL,
  STATE_LABELS,
  label,
} from "../labels.ts";
import type { WorkspaceData } from "../live/useLive.ts";
import type { AgentBoard, Challenge, RunningChallenge } from "./types.ts";

// 新しい API の課題 1 件を board のカードの形へ写す。
export function toChallenge(c: ChallengeCard): Challenge {
  const actions = c.card.next_human_actions;
  const modifiers = c.card.modifiers;
  const stateText = [
    label(STATE_LABELS, c.card.state),
    ...modifiers.map((m) => `[${label(MODIFIER_LABELS, m)}]`),
  ].join(" ");
  return {
    id: c.id,
    title: c.title,
    status: label(STATE_LABELS, c.card.state),
    state: c.card.state,
    priority: c.priority ?? undefined,
    needsHuman: actions.length > 0,
    summary: c.card.headline ?? undefined,
    description: c.description === "" ? undefined : c.description,
    completionCriteria: c.done_criteria === "" ? undefined : c.done_criteria,
    oneLiner: c.card.headline ?? undefined,
    positioning: c.card.context ?? undefined,
    currentState: stateText,
    nextHumanAction:
      actions.length === 0
        ? NO_ACTION_LABEL
        : actions.map((a) => label(ACTION_LABELS, a)).join("、"),
    modifiers,
    nextHumanActions: actions,
  };
}

// 人間の操作待ちを先頭に、各グループ内は優先度の昇順（無いものは末尾）。同順位は元の並びを保つ。
export function sortChallenges(list: Challenge[]): Challenge[] {
  const group = (c: Challenge) => (c.needsHuman ? 0 : 1);
  const hasPriority = (c: Challenge) => (c.priority === undefined ? 1 : 0);
  return [...list].sort((a, b) => {
    if (group(a) !== group(b)) return group(a) - group(b);
    if (hasPriority(a) !== hasPriority(b))
      return hasPriority(a) - hasPriority(b);
    if (a.priority !== undefined && b.priority !== undefined) {
      if (a.priority < b.priority) return -1;
      if (a.priority > b.priority) return 1;
    }
    return 0;
  });
}

// ワークスペース 1 つを board の「エージェントの列」へ写す。state が ok でないものは列にエラーを出す。
export function toAgentBoard(
  w: Workspace,
  d: WorkspaceData | undefined,
): AgentBoard {
  const parseErrors = [];
  if (w.state !== "ok") {
    parseErrors.push({
      file: w.path,
      message: `${w.state}${w.error ? `: ${w.error}` : ""}`,
      raw: "",
    });
  } else if (d?.error) {
    parseErrors.push({ file: w.path, message: d.error, raw: "" });
  }
  // 読めないワークスペースは、前に読めた課題を残さずエラーだけを出す（S1 の画面と同じ）。
  const challenges =
    w.state !== "ok"
      ? []
      : sortChallenges((d?.challenges?.challenges ?? []).map(toChallenge));
  const runningRuns: RunningChallenge[] = challenges
    .filter(
      (c) =>
        c.modifiers.includes("running") || c.modifiers.includes("unresponsive"),
    )
    .map((c) => ({
      challengeId: c.id,
      title: c.title,
      unresponsive: c.modifiers.includes("unresponsive"),
    }));
  return { name: w.name, path: w.path, challenges, parseErrors, runningRuns };
}
