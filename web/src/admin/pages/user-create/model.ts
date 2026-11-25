import type { Schemas } from "../../../api/client";
import { parseTags } from "../../../components/tags";

export type Template = Schemas["UserTemplateView"];
export type TemplateBody = Schemas["UserTemplateBody"];
export type BatchUser = Schemas["BatchUser"];
/** What a new client gets over its tariff, as the API takes it. */
export type Spec = Omit<TemplateBody, "name" | "name_pattern">;

/** A limit of the form: undefined is the tariff's own, null none at all (∞), a number its own. */
export type Limit = number | null | undefined;

/** The form of a new client, one or a batch: what goes over the tariff, and the batch's names. */
export type Draft = {
  tariffId?: number;
  traffic: Limit; // bytes a period
  devices: Limit;
  speed: Limit; // Mbit/s each way
  term: Limit; // days; null never ends
  /** Whether the term waits for the first connection; undefined: as the tariff says. */
  wait?: boolean;
  folderId: number | null;
  /** The client's own group; null: its tariff's. */
  groupId: number | null;
  tags: string; // the labels field: words by commas
  note: string;
  pattern: string;
};

/** The pattern a batch is named by when its template has none. */
export const DEFAULT_PATTERN = "client-{n}";

export const emptyDraft = (tariffId?: number): Draft => ({
  tariffId,
  traffic: undefined,
  devices: undefined,
  speed: undefined,
  term: undefined,
  folderId: null,
  groupId: null,
  tags: "",
  note: "",
  pattern: DEFAULT_PATTERN,
});

/**
 * The tariff a new client starts with: of the ones a new panel has (Trial, Standard,
 * Unlimited) the usual choice for a customer is Standard, the second, not the 3-day trial;
 * with one tariff only, that one.
 */
export const defaultTariffId = (list?: { id: number }[]) => (list?.length ? list[Math.min(1, list.length - 1)]!.id : undefined);

// The API says a limit with null for the tariff's own and 0 for none; the form the other way.
function limitOf(v: number | null): Limit {
  if (v === null) return undefined;
  return v === 0 ? null : v;
}

function limitBody(v: Limit): number | undefined {
  if (v === undefined) return undefined;
  return v ?? 0;
}

export function draftOf(t: Template): Draft {
  return {
    tariffId: t.tariff_id,
    traffic: limitOf(t.traffic_limit),
    devices: limitOf(t.device_limit),
    speed: limitOf(t.speed_limit),
    term: limitOf(t.term_days),
    wait: t.starts_on_connect ?? undefined,
    folderId: t.folder_id,
    groupId: t.group_id,
    tags: t.tags.join(", "),
    note: t.note,
    pattern: t.name_pattern || DEFAULT_PATTERN,
  };
}

/** What d sends for every client it makes: a field the form leaves to the tariff is left out. */
export function specOf(d: Draft): Spec {
  return {
    tariff_id: d.tariffId ?? 0,
    traffic_limit: limitBody(d.traffic),
    device_limit: limitBody(d.devices),
    speed_limit: limitBody(d.speed),
    term_days: limitBody(d.term),
    starts_on_connect: d.wait,
    folder_id: d.folderId ?? undefined,
    group_id: d.groupId ?? undefined,
    tags: parseTags(d.tags),
    note: d.note.trim() || undefined,
  };
}

export const templateBody = (d: Draft, name: string): TemplateBody => ({ name, name_pattern: d.pattern.trim() || undefined, ...specOf(d) });

/** Whether saving d over the template t would change it: what the button "update" offers. */
export function draftDiffers(d: Draft, t: Template): boolean {
  return JSON.stringify(templateBody(d, t.name)) !== JSON.stringify(templateBody(draftOf(t), t.name));
}

// Where a batch's name pattern puts each client's number, as the API reads it
// (domain.BatchNames): {n}, or {nnn} for at least as many digits.
const NUMBER_MARK = /\{(n+)\}/g;

// search, unlike test, ignores the lastIndex a global expression keeps between calls.
export const hasNumber = (pattern: string) => pattern.search(NUMBER_MARK) !== -1;

/** The name of client n of a batch named by pattern. */
export const batchName = (pattern: string, n: number) => pattern.trim().replace(NUMBER_MARK, (_, marks: string) => String(n).padStart(marks.length, "0"));

/** The first and the last name of count clients from start on. */
export const batchRange = (pattern: string, start: number, count: number) => ({ first: batchName(pattern, start), last: batchName(pattern, start + Math.max(count, 1) - 1) });

/** The clients a batch made, a line each with the link: to send as they are. */
export const linksText = (users: BatchUser[]) => users.map((u) => `${u.name} — ${u.sub_url}`).join("\n");

// A cell a spreadsheet would read as a formula (=, +, -, @, a tab or a return first) is kept
// as text by a quote in front of it.
function csvCell(s: string): string {
  const text = /^[=+\-@\t\r]/.test(s) ? `'${s}` : s;
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** The clients a batch made as a table to open in a spreadsheet. */
export const csvOf = (users: BatchUser[]) => ["name,link", ...users.map((u) => `${csvCell(u.name)},${csvCell(u.sub_url)}`)].join("\r\n") + "\r\n";
