import { CONTENT, MONTHS, type Bank, type SectionBank } from "./content";
import { KINDS, newId, type BlockData, type Kind, type Lang, type Page, type SiteDoc } from "./model";
import { Rng } from "./rng";
import { randomTheme } from "./themes";

/**
 * Makes a whole site at random: its kind, its name, the words of every page and its looks.
 * The same seed gives the same site, so an installation opens its builder on a site of its
 * own and "random" draws another with a fresh seed.
 */
export class SiteGenerator {
  private rng!: Rng;
  private lang!: Lang;
  private brand = "";

  random(seed: string, lang: Lang, kind?: Kind): SiteDoc {
    this.rng = new Rng("doc:" + seed);
    this.lang = lang;
    const k = kind ?? this.rng.pick(KINDS);
    const content = CONTENT[k];
    const bank = content[lang];
    this.brand = Array.isArray(bank.names[0]) ? this.rng.pick(bank.names[0] as string[]) + this.rng.pick(bank.names[1] as string[]) : this.rng.pick(bank.names as string[]);
    const used = new Set<number>();
    const pages: Page[] = content.pages.map((plan) => ({
      id: newId("p"),
      path: plan.path,
      title: plan.title[lang],
      blocks: plan.sections.map((i) => {
        const again = used.has(i);
        used.add(i);
        return this.section(bank.sections[i]!, again);
      }),
    }));
    const second = pages[1];
    pages[0]!.blocks.unshift(this.hero(bank, second));
    return { v: 2, lang, kind: k, brand: this.brand, tag: this.rng.pick(bank.tags), foot: this.rng.pick(bank.foot), seed, theme: randomTheme(k, this.rng), pages, images: {} };
  }

  private named(text: string): string {
    return text.replaceAll("{name}", this.brand);
  }

  private hero(bank: Bank, next?: Page): BlockData {
    const [title, text] = this.rng.pick(bank.heroes);
    return { id: newId(), t: "hero", title: this.named(title), text, cta: next ? next.title : "", page: next ? next.path : "", image: "" };
  }

  /** A section of the bank as a block. Asked for again (a page that lists all of what the first page showed a few of), it is given whole. */
  private section(s: SectionBank, whole: boolean): BlockData {
    const r = this.rng;
    const title = r.pick(s.title);
    const id = newId();
    switch (s.t) {
      case "list": {
        // Steps follow one another: those taken keep the order they were written in.
        const picked = whole
          ? s.pool
          : s.style === "steps"
            ? r
                .take(s.pool.map((it, i) => [i, it] as const), s.take)
                .sort((a, b) => a[0] - b[0])
                .map(([, it]) => it)
            : r.take(s.pool, s.take);
        const when = s.dated ? this.dates(picked.length) : [];
        return { id, t: "list", title, style: s.style, items: picked.map(([t, text, meta], i) => ({ title: t, text, meta: when[i] ?? meta ?? "" })) };
      }
      case "prose":
        return { id, t: "prose", title, paras: whole ? s.pool : r.shuffled(s.pool).slice(0, s.take) };
      case "table": {
        // A table reads in the order it was written (a changelog, a schedule): a run of it.
        const n = whole ? s.pool.length : r.int(s.take[0], s.take[1]);
        const from = r.int(0, Math.max(0, s.pool.length - n));
        return { id, t: "table", title, rows: s.pool.slice(from, from + n).map(([name, note, value]) => ({ name, note, value })) };
      }
      case "gallery":
        return { id, t: "gallery", title, works: (whole ? s.pool : r.take(s.pool, s.take)).map((w) => ({ title: w })) };
      case "code":
        return { id, t: "code", title, lines: r.pick(s.pool).map((l) => this.named(l)) };
    }
  }

  /** Dates going back from a few days ago, a few weeks apart: what a blog nobody hurries has. */
  private dates(n: number): string[] {
    const out: string[] = [];
    const at = new Date();
    at.setDate(at.getDate() - this.rng.int(4, 25));
    for (let i = 0; i < n; i++) {
      out.push(`${at.getDate()} ${MONTHS[this.lang][at.getMonth()]} ${at.getFullYear()}`);
      at.setDate(at.getDate() - this.rng.int(9, 47));
    }
    return out;
  }
}
