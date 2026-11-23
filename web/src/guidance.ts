import guidance from "./guidance.json";

// 案内を出す場面。課題の操作（next_human_actions のコード）に結び付く案内は actions で、
// 結び付かない案内（取り込み・フォローアップなど）は、それを出す画面の名前 context で書く。どちらか一方だけを持つ。
export interface When {
  actions?: string[];
  context?: string;
  // 回答の相手の保留を --hold で指定する場面かどうかによる出し分け（無ければ絞らない）。開いている保留が 2 つ以上のとき真。
  needs_hold?: boolean;
}

export interface Template {
  id: string;
  when: When;
  template: string;
  note?: string;
}

export const OUTSIDE_NOTE: string = guidance.outside_note;
export const CONVERSATION_NOTE: string = guidance.conversation_note;

// 承認の画面の案内。APPROVAL_REJECT_NOTE は完了承認画面と計画の承認の画面の両方の差し戻しの案内に使う。対話で伝える方法だけを出す（コマンドの例は出さない）。
export const COMPLETION_BUTTON_NOTE: string = guidance.completion_button_note;
export const COMPLETION_RELEASE_NOTE: string = guidance.completion_release_note;
export const APPROVAL_REJECT_NOTE: string = guidance.completion_reject_note;
export const COMPLETION_APPROVED_NOTE: string =
  guidance.completion_approved_note;

// 承認の後に出す、サイクルを始めたかの 1 行。cycle.reason（started・already_running・no_work・disabled・unavailable）から選ぶ。
// 知らない値は unavailable の文にする（始められたと言い切らない）。
export function cycleStartNote(reason: string): string {
  const notes: Record<string, string | undefined> = guidance.cycle_start_notes;
  return notes[reason] ?? guidance.cycle_start_notes.unavailable;
}

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

// card.next_human_actions のコードから、ターミナルで実行するコマンドの案内を作る（課題の状態の文字列は見ない）。
// <理由>・<回答>・<額>・<OP-ID> は差し込み口のまま残す。
// 回答の相手の保留を指定する場面（needsHold。既定は openHoldIds が 2 つ以上）の回答の案内（<H-ID> を持つ雛形）は、openHoldIds の保留ごとに 1 つ作り、<H-ID> をその保留の ID にする。
export function guidesFor(
  challengeId: string,
  workspacePath: string,
  actions: string[],
  openHoldIds: string[] = [],
  needsHold: boolean = openHoldIds.length >= 2,
): Guide[] {
  return (guidance.templates as Template[])
    .filter(
      (t) =>
        t.when.actions?.some((a) => actions.includes(a)) &&
        (t.when.needs_hold === undefined || t.when.needs_hold === needsHold),
    )
    .flatMap((t) => {
      const base = t.template
        .replaceAll("<C-ID>", () => challengeId)
        .replaceAll("<パス>", () => shellQuote(workspacePath));
      const note = t.note ? { note: t.note } : {};
      if (!base.includes("<H-ID>")) {
        return [{ id: t.id, command: base, ...note }];
      }
      return openHoldIds.map((h) => ({
        id: `${t.id}-${h}`,
        command: base.replaceAll("<H-ID>", () => h),
        ...note,
      }));
    });
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
