import guidance from "./guidance.json";

interface Template {
  id: string;
  when: { statuses?: string[]; modifiers?: string[] };
  template: string;
}

export const OUTSIDE_NOTE: string = guidance.outside_note;

// 単一引用符で囲み、中の単一引用符は '\'' にする（空白・引用符を含むパスを 1 語として渡すため）。
export function shellQuote(path: string): string {
  return `'${path.replaceAll("'", "'\\''")}'`;
}

export interface Guide {
  id: string;
  command: string;
}

// 課題の状態と修飾から、端末で実行するコマンドの案内を作る。<回答>・<額> は差し込み口のまま残す。
export function guidesFor(
  challengeId: string,
  workspacePath: string,
  status: string,
  modifiers: string[],
): Guide[] {
  return (guidance.templates as Template[])
    .filter(
      (t) =>
        t.when.statuses?.includes(status) ||
        t.when.modifiers?.some((m) => modifiers.includes(m)),
    )
    .map((t) => ({
      id: t.id,
      command: t.template
        .replaceAll("<C-ID>", () => challengeId)
        .replaceAll("<パス>", () => shellQuote(workspacePath)),
    }));
}
