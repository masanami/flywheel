import { NOTICE_BADGE, type NoticeLine } from "./notices.ts";
import { shortTime } from "./when.ts";

// 知らせの 1 行。「最近起きたこと」の枠と対話の欄で同じ形を使う（札は欄でだけ付ける）。
export function NoticeItem({
  line,
  badge,
}: {
  line: NoticeLine;
  badge?: boolean;
}) {
  return (
    <li className="notice-item" data-testid="notice-item">
      {badge ? (
        <span className="notice-badge" data-testid="notice-badge">
          {NOTICE_BADGE}
        </span>
      ) : null}{" "}
      <span className="now-hint">{shortTime(line.at)}</span>{" "}
      {line.href ? (
        <a href={line.href}>{line.text}</a>
      ) : (
        <span>{line.text}</span>
      )}
      {line.guide ? (
        <div className="now-hint" data-testid="notice-guide">
          {line.guide}
        </div>
      ) : null}
    </li>
  );
}
