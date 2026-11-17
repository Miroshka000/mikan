import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Hero = BlockData & { title: string; text: string; cta: string; page: string; image: string };

/** The first screen: what the site is, a few words more, and a link further in. */
export class HeroBlock extends BlockKind<Hero> {
  readonly type = "hero";
  readonly group = "structure" as const;
  override readonly titled = false;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 160 },
    { key: "text", kind: "area", max: 600 },
    { key: "cta", kind: "text", max: 40 },
    { key: "page", kind: "page" },
    { key: "image", kind: "image" },
  ];

  empty(lang: Lang) {
    return lang === "ru" ? { title: "Заголовок страницы", text: "Пара предложений о том, что это за сайт.", cta: "", page: "", image: "" } : { title: "The page's heading", text: "A sentence or two on what this site is.", cta: "", page: "", image: "" };
  }

  render(b: Hero, c: RenderContext): string {
    const img = c.image(b.image);
    const more = str(b.cta).trim() ? `<p><a class="${c.cls("more")}" href="${esc(c.href(str(b.page)))}"${c.e("cta")}>${esc(b.cta)}</a></p>` : "";
    const pic = img ? `<img class="${c.cls("pic")}" src="${esc(img.src)}" width="${img.w}" height="${img.h}" alt="">` : "";
    return `<div class="${c.cls("hero")}${img ? " " + c.cls("split") : ""}"><div><h1${c.e("title")}>${esc(b.title)}</h1><p class="${c.cls("lead")}"${c.e("text")}>${esc(b.text)}</p>${more}</div>${pic}</div>`;
  }

  styles(s: StyleContext): string[] {
    const big = s.rng.pick([48, 52, 56, 60]);
    return [
      `.${s.cls("hero")}{padding-block:${s.unit * 3}px ${s.unit * 2.5}px}`,
      `.${s.cls("split")}{display:grid;grid-template-columns:minmax(0,${s.rng.pick(["1.1fr", "1.2fr", "1fr"])}) minmax(0,1fr);gap:${s.unit * 2}px;align-items:center}`,
      `.${s.cls("hero")} h1{font-family:${s.heading};font-size:clamp(32px,5.4vw,${big}px);line-height:1.08;letter-spacing:-.02em;font-weight:${s.rng.pick([600, 700, 800])};margin:0 0 ${s.unit}px;max-width:18ch;text-wrap:balance}`,
      `.${s.cls("split")} h1{max-width:none}`,
      `.${s.cls("lead")}{font-size:1.15em;line-height:1.55;color:var(--mu);margin:0 0 ${s.unit}px;max-width:54ch}`,
      `.${s.cls("more")}{display:inline-block;padding:13px 22px;border-radius:${s.radius}px;background:var(--ac);color:var(--on);font-weight:600;text-decoration:none}`,
      `.${s.cls("more")}:hover,.${s.cls("more")}:focus-visible{filter:brightness(1.08)}`,
      `.${s.cls("pic")}{display:block;width:100%;height:auto;border-radius:${s.radius}px}`,
      `.${s.cls("band")}{background:var(--ac);color:var(--on)}`,
      `.${s.cls("band")} .${s.cls("lead")}{color:inherit;opacity:.86}`,
      `.${s.cls("band")} .${s.cls("more")}{background:var(--on);color:var(--ac)}`,
    ];
  }
}
