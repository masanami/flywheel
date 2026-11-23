import type { CycleWork } from "../api/types.gen.ts";
import { guidesForContext } from "../guidance.ts";
import { CYCLE_WORK_STAGES, CYCLE_WORK_STAGE_LABELS } from "../labels.ts";
import { CycleStartButton } from "./CycleStartButton.tsx";

export const NO_CYCLE_WORK_TEXT =
  "進められるものが無いので、サイクルは始めません。";

// サイクルを回すと進むもの（合計と段ごとの件数）と「サイクルを回す」のボタン。
// 「いま進められること」とサイクルの画面が、同じこの部品を使う。件数は API の値をそのまま出す。
// 止まっているかもしれないときは、画面からは始められないので、ターミナルのコマンドの案内を出す。
export function CycleWorkPart({
  workspace,
  workspacePath,
  work,
  cycleState,
  hideStages = false,
}: {
  workspace: string;
  workspacePath: string | undefined;
  work: CycleWork | undefined;
  cycleState: string;
  // 段ごとの件数を、呼ぶ側が別の形で出すとき。
  hideStages?: boolean;
}) {
  const cycleGuide =
    workspacePath === undefined
      ? undefined
      : guidesForContext("cycle", workspacePath)[0];
  return (
    <section
      aria-labelledby="cycle-work-title"
      className="now-block"
      data-testid="cycle-work"
    >
      <h3 id="cycle-work-title">
        サイクルを回すと進むもの（
        <span data-testid="cycle-work-total">{work ? work.total : "—"}</span>{" "}
        件）
      </h3>
      {!hideStages && work && work.total > 0 ? (
        <ul className="now-stages" data-testid="cycle-stages">
          {CYCLE_WORK_STAGES.filter((s) => work.stages[s] > 0).map((s) => (
            <li key={s} data-stage={s}>
              {CYCLE_WORK_STAGE_LABELS[s]} <b>{work.stages[s]}</b>
            </li>
          ))}
        </ul>
      ) : null}
      {work && work.total === 0 ? <p>{NO_CYCLE_WORK_TEXT}</p> : null}
      <CycleStartButton
        workspace={workspace}
        total={work?.total ?? 0}
        cycleState={cycleState}
      />
      {cycleState === "stale" && cycleGuide ? (
        <p className="now-hint">
          サイクルが止まっているかもしれないので、この画面からは始められません。ターミナルで次のコマンドを実行すると、止まったサイクルを片付けてから始めます:{" "}
          <code className="now-command">{cycleGuide.command}</code>
        </p>
      ) : null}
    </section>
  );
}
