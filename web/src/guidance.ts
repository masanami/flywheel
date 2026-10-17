import guidance from "./guidance.json";

interface Template {
  id: string;
  when: { actions: string[] };
  template: string;
  note?: string;
}

export const OUTSIDE_NOTE: string = guidance.outside_note;
export const CONVERSATION_NOTE: string = guidance.conversation_note;

// 単一引用符で囲み、中の単一引用符は '\'' にする（空白・引用符を含むパスを 1 語として渡すため）。
export function shellQuote(path: string): string {
  return `'${path.replaceAll("'", "'\\''")}'`;
}

export interface Guide {
  id: string;
  command: string;
  note?: string;
}

// 対話で伝えれば LLM が記録できる操作のコード。triage・review_discrepancy・clear_slot は含まない。
export function showsConversationNote(actions: string[]): boolean {
  return actions.some((a) => guidance.conversation_actions.includes(a));
}

// card.next_human_actions のコードから、端末で実行するコマンドの案内を作る（課題の状態の文字列は見ない）。
// <理由>・<回答>・<額>・<OP-ID> は差し込み口のまま残す。
export function guidesFor(
  challengeId: string,
  workspacePath: string,
  actions: string[],
): Guide[] {
  return (guidance.templates as Template[])
    .filter((t) => t.when.actions.some((a) => actions.includes(a)))
    .map((t) => ({
      id: t.id,
      command: t.template
        .replaceAll("<C-ID>", () => challengeId)
        .replaceAll("<パス>", () => shellQuote(workspacePath)),
      ...(t.note ? { note: t.note } : {}),
    }));
}
