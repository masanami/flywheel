import guidance from "./guidance.json";

// 案内を出す場面。課題の操作（next_human_actions のコード）に結び付く案内は actions で、
// 結び付かない案内（取り込み・フォローアップなど）は、それを出す画面の名前 context で書く。どちらか一方だけを持つ。
export interface When {
  actions?: string[];
  context?: string;
}

export interface Template {
  id: string;
  when: When;
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
    .filter((t) => t.when.actions?.some((a) => actions.includes(a)))
    .map((t) => ({
      id: t.id,
      command: t.template
        .replaceAll("<C-ID>", () => challengeId)
        .replaceAll("<パス>", () => shellQuote(workspacePath)),
      ...(t.note ? { note: t.note } : {}),
    }));
}

// 課題の操作に結び付かない案内を、場面の名前（context）から作る。差し込み口（<理由> など）はそのまま残す。
export function guidesForContext(
  context: string,
  workspacePath: string,
  templates: Template[] = guidance.templates as Template[],
): Guide[] {
  return templates
    .filter((t) => t.when.context === context)
    .map((t) => ({
      id: t.id,
      command: t.template.replaceAll("<パス>", () => shellQuote(workspacePath)),
      ...(t.note ? { note: t.note } : {}),
    }));
}
