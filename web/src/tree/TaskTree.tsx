import {
  PHASE_LABELS,
  TASK_ADDED_TREE_MARK,
  TASK_PROGRESS_LABELS,
  label,
} from "../labels.ts";
import {
  BOX_H,
  BOX_W,
  type PlacedTask,
  type TreeNode,
  layoutTree,
} from "./layout.ts";

export const NO_TASKS_TEXT = "タスクはまだありません。";

// タスクのツリー。課題の画面・課題の詳細・計画の承認の画面が使う、画面から独立した部品。
// 箱の区分（phase）は core が決めた値をそのまま属性と文字で出す。id があるときだけリンクにする。
export function TaskTree({
  tasks,
  taskHref,
}: {
  tasks: readonly TreeNode[];
  // タスクの id からその詳細への href を作る（id が null の箱はリンクにしない）。
  taskHref?: (taskId: string) => string;
}) {
  if (tasks.length === 0) {
    return <p className="tree-empty">{NO_TASKS_TEXT}</p>;
  }
  const lay = layoutTree(tasks);
  return (
    <div className="tree-scroll" data-testid="task-tree">
      <div
        className="tree-canvas"
        style={{ width: lay.width, height: lay.height }}
      >
        <svg
          className="tree-edges"
          width={lay.width}
          height={lay.height}
          aria-hidden="true"
        >
          {lay.edges.map((e, i) => (
            <path
              // 同じ組が重複して書かれても線は 1 つずつ出す
              key={`${e.from}>${e.to}#${i}`}
              d={e.d}
              data-edge={`${e.from}>${e.to}`}
            />
          ))}
        </svg>
        {lay.boxes.map((b) => (
          <TaskBox
            key={b.task.key}
            box={b}
            href={b.task.id != null && taskHref ? taskHref(b.task.id) : null}
          />
        ))}
      </div>
    </div>
  );
}

function TaskBox({ box, href }: { box: PlacedTask; href: string | null }) {
  const { task } = box;
  const common = {
    className: "task-box",
    "data-testid": "task-box",
    "data-phase": task.phase,
    "data-added": task.added === true ? "true" : undefined,
    "data-col": box.col + 1,
    style: { left: box.x, top: box.y, width: BOX_W, height: BOX_H },
  };
  const inner = (
    <>
      <span className="task-title">{task.title}</span>
      <small className="task-phase">
        {label(PHASE_LABELS, task.phase)}
        {task.progress != null ? (
          <>
            {" ・ "}
            <span className="task-progress" data-testid="task-progress">
              {label(TASK_PROGRESS_LABELS, task.progress)}
            </span>
          </>
        ) : null}
        {task.added === true ? (
          <>
            {" ・ "}
            <span className="task-added-mark" data-testid="task-box-added">
              {TASK_ADDED_TREE_MARK}
            </span>
          </>
        ) : null}
      </small>
    </>
  );
  return href !== null ? (
    <a {...common} href={href}>
      {inner}
    </a>
  ) : (
    <div {...common}>{inner}</div>
  );
}
