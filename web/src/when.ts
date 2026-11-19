// 日時の短い書き方。ブラウザのいまの時間帯で「月/日 時:分」に整える（正確な値は time の dateTime に持つ）。
// 読めない値は、そのまま返す。
export function shortTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
