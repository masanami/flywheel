import type { CycleWork } from "../api/types.gen.ts";
import { CYCLE_STAGES, CYCLE_STAGE_LABELS } from "../labels.ts";
import { CycleStartButton } from "../shell/CycleStartButton.tsx";

// サイクルを回すと進むもの（合計と段ごとの件数）と「サイクルを回す」のボタン。
// サイクルの画面が使い、「いま進められること」の区分も同じ部品を使う予定。件数は API の値をそのまま出す。
export function CycleWorkPart({
  workspace,
  work,
  cycleState,
}: {
  workspace: string;
  work: CycleWork | undefined;
  cycleState: string;
}) {
  return (
    <div className="cycle-work" data-testid="cycle-work">
      <p>
        サイクルを回すと進むもの 合計{" "}
        <b data-testid="cycle-work-total">{work ? work.total : "—"}</b> 件
      </p>
      {work ? (
        <ul className="cycle-work-stages">
          {CYCLE_STAGES.filter((s) => work.stages[s] > 0).map((s) => (
            <li key={s} data-stage={s}>
              {CYCLE_STAGE_LABELS[s]} <b>{work.stages[s]}</b>
            </li>
          ))}
        </ul>
      ) : null}
      {work && work.total === 0 ? (
        <p>進められるものが無いので、サイクルは始めません。</p>
      ) : null}
      {cycleState === "stale" ? (
        <p>
          止まっているかもしれません。端末で flywheel cycle
          を実行すると、止まったサイクルを片付けてから始めます（画面からは片付けられません）。
        </p>
      ) : null}
      <CycleStartButton
        workspace={workspace}
        total={work?.total ?? 0}
        cycleState={cycleState}
      />
    </div>
  );
}
