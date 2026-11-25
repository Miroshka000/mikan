import { PlotArt } from "../art";
import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Work = { title: string; image?: string };
export type Gallery = BlockData & { title: string; works: Work[] };

/** A grid of pictures with captions: the admin's own, or a line drawing of the site's where there is none. */
export class GalleryBlock extends BlockKind<Gallery> {
  readonly type = "gallery";
  readonly group = "media" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "works",
      kind: "items",
      of: [
        { key: "title", kind: "text", max: 80 },
        { key: "image", kind: "image" },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Галерея", "Работа"] : ["Gallery", "Work"];
    return { title: w[0]!, works: [1, 2, 3].map((n) => ({ title: `${w[1]} ${n}` })) };
  }

  render(b: Gallery, c: RenderContext): string {
    const p = c.doc.theme.palette;
    const works = arr<Work>(b.works).map((work, i) => {
      const img = c.image(work.image);
      const pic = img
        ? `<img src="${esc(img.src)}" width="${img.w}" height="${img.h}" alt="${esc(str(work.title))}" loading="lazy">`
        : new PlotArt(c.rng(`work:${i}:${str(work.title)}`), { ink: p.ink, accent: p.accent, surface: p.surface }).svg(str(work.title));
      return `<figure>${pic}<figcaption${c.e(`works.${i}.title`)}>${esc(str(work.title))}</figcaption></figure>`;
    });
    return `<div class="${c.cls("works")}">${works.join("")}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("works")}{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,${s.rng.pick([220, 240, 260])}px),1fr));gap:${s.unit}px}`,
      `.${s.cls("works")} figure{margin:0}`,
      `.${s.cls("works")} svg,.${s.cls("works")} img{display:block;width:100%;height:auto;aspect-ratio:4/3;object-fit:cover;border:1px solid var(--ln);border-radius:${s.radius}px}`,
      `.${s.cls("works")} figcaption{margin-top:8px;font-size:.85em;color:var(--mu)}`,
    ];
  }
}
