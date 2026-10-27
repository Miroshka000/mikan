// Labels of a node or a protocol, as a user has them: chips on the card, a field of words
// split by commas, and a filter of the list by one of them.
import { t } from "../i18n";
import { Segmented } from "./ui";

/** As many labels as the API takes (maxItems of tags). */
export const MAX_TAGS = 20;

/** The words of a labels field: split by commas, trimmed, the empty ones and repeats dropped. */
export function parseTags(s: string): string[] {
  const out: string[] = [];
  for (const raw of s.split(",")) {
    const tag = raw.trim();
    if (tag && !out.some((x) => x.toLowerCase() === tag.toLowerCase())) out.push(tag);
  }
  return out;
}

/** The item has the label, in any letter case: as the API's ?tag= filter. */
export const hasTag = (tags: string[], tag: string) => tags.some((x) => x.toLowerCase() === tag.toLowerCase());

/** The labels as chips, nothing when there are none. */
export function TagChips({ tags }: { tags: string[] }) {
  if (tags.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {tags.map((tag) => (
        <span key={tag} className="tag">
          {tag}
        </span>
      ))}
    </span>
  );
}

/** One label to show only what has it; "" is all. Nothing to choose from, nothing shown. */
export function TagFilter({ items, value, onChange }: { items: { tags: string[] }[]; value: string; onChange: (tag: string) => void }) {
  const all: string[] = [];
  for (const i of items) for (const tag of i.tags) if (!hasTag(all, tag)) all.push(tag);
  if (all.length === 0) return null;
  all.sort((a, b) => a.localeCompare(b));
  return (
    <div className="max-w-full overflow-x-auto">
      <Segmented label={t("tags.filter")} value={value} onChange={onChange} options={[{ value: "", label: t("tags.all") }, ...all.map((tag) => ({ value: tag, label: tag }))]} />
    </div>
  );
}
