export function NotFound({ homeHref }: { homeHref: string }) {
  return (
    <section>
      <p role="alert">見つかりません。</p>
      <a href={homeHref}>「いま進められること」へ</a>
    </section>
  );
}
