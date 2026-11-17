// What a random site is made of: for each kind of small site, in each language, the names,
// the words of the first screen and pools the sections draw from; and the pages of the kind.
// Nothing here claims a fact about a real company: no reviews, no counts of clients, no
// addresses or phones.

import type { Lang, ListStyle } from "../model";

/** title, text, a short note beside the title (a date, a time, a level) */
export type Item = [string, string, string?];
/** name, note, value */
export type Row = [string, string, string];

export type SectionBank =
  | { t: "list"; title: string[]; style: ListStyle; take: [number, number]; pool: Item[]; dated?: boolean }
  | { t: "prose"; title: string[]; take: number; pool: string[] }
  | { t: "table"; title: string[]; take: [number, number]; pool: Row[] }
  | { t: "gallery"; title: string[]; take: [number, number]; pool: string[] }
  | { t: "code"; title: string[]; pool: string[][] };

export type Bank = {
  /** Whole names, or two lists to join one of each from (a tool's name). */
  names: string[] | [string[], string[]];
  tags: string[];
  heroes: [string, string][];
  foot: string[];
  sections: SectionBank[];
};

/** A page of a kind: its address ("" is the first page), its name in the menu, and the bank's sections it holds. */
export type PagePlan = { path: string; title: Record<Lang, string>; sections: number[] };

export type KindContent = { pages: PagePlan[] } & Record<Lang, Bank>;

export const MONTHS: Record<Lang, string[]> = {
  ru: ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"],
  en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
};
