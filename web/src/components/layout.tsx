import clsx from "clsx";
import { ChevronRight, Eye } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useId, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { t } from "../i18n";
import { useMediaQuery } from "../lib/media";
import { Drawer } from "./overlay";
import { Button } from "./ui";
import type { LucideIcon } from "lucide-react";

/** A section of a page in the side list; `parts` are its own sub-sections, listed under it while it is open. */
type NavSection<T extends string> = { id: T; label: string; icon?: LucideIcon; badge?: ReactNode; parts?: { id: string; label: string }[] };

/**
 * A page with many sections: a list beside the content on wide screens (it stays in place
 * while the content scrolls), a select above it on narrow ones, so nothing scrolls sideways
 * and the content always starts at the same place. The caller keeps the value (in the URL),
 * `part` is the open sub-section of a section that has them.
 */
export function SectionNav<T extends string>({
  label,
  sections,
  value,
  part,
  onChange,
  narrow = true,
  children,
}: {
  label: string;
  sections: NavSection<T>[];
  value: T;
  part?: string;
  onChange: (section: T, part?: string) => void;
  /** Forms keep to a readable width; an editor with its preview takes the whole column. */
  narrow?: boolean;
  children: ReactNode;
}) {
  const wide = useMediaQuery("(min-width: 1024px)");
  const selectId = useId();
  const current = sections.find((s) => s.id === value);
  const currentPart = current?.parts?.find((p) => p.id === part);
  const title = currentPart ? `${current?.label}: ${currentPart.label}` : current?.label;
  const body = useRef<HTMLElement>(null);
  const packed = useMediaQuery(MASONRY);
  useMasonry(body, narrow && packed);
  return (
    <div className="section-layout">
      {wide ? (
        <nav className="section-nav" aria-label={label}>
          <ul>
            {sections.map((s) => {
              const open = s.id === value;
              const Icon = s.icon;
              return (
                <li key={s.id}>
                  <button
                    type="button"
                    aria-current={open && !s.parts ? "page" : undefined}
                    data-open={open && s.parts ? "true" : undefined}
                    aria-expanded={s.parts ? open : undefined}
                    onClick={() => onChange(s.id, s.parts ? (open ? part : s.parts[0]?.id) : undefined)}
                  >
                    {Icon ? <Icon size={18} aria-hidden /> : null}
                    <span className="min-w-0 flex-1 truncate">{s.label}</span>
                    {s.badge ? <span className="section-nav-badge">{s.badge}</span> : null}
                  </button>
                  {open && s.parts ? (
                    <ul className="sub">
                      {s.parts.map((p) => (
                        <li key={p.id}>
                          <button type="button" aria-current={p.id === part ? "page" : undefined} onClick={() => onChange(s.id, p.id)}>
                            <span className="min-w-0 flex-1 truncate">{p.label}</span>
                          </button>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </nav>
      ) : (
        <div className="field !mb-0">
          <label htmlFor={selectId}>{label}</label>
          <select
            id={selectId}
            className="input"
            value={current?.parts ? `${value}:${part ?? ""}` : value}
            onChange={(e) => {
              const [s, p] = e.target.value.split(":") as [T, string | undefined];
              onChange(s, p || undefined);
            }}
          >
            {sections.map((s) =>
              s.parts ? (
                <optgroup key={s.id} label={s.label}>
                  {s.parts.map((p) => (
                    <option key={p.id} value={`${s.id}:${p.id}`}>
                      {p.label}
                    </option>
                  ))}
                </optgroup>
              ) : (
                <option key={s.id} value={s.id}>
                  {s.label}
                </option>
              ),
            )}
          </select>
        </div>
      )}
      <section ref={body} className={clsx("section-body", narrow && "narrow")} aria-label={title}>
        {children}
      </section>
    </div>
  );
}

/** Where a section's cards go two in a row (app.css, .section-body.narrow). */
const MASONRY = "(min-width: 1440px)";
/** The grid's row unit and the gap between cards, as in app.css. */
const ROW = 4;
const GAP = 16;

/**
 * Packs the cards of a two-column grid without holes: each card spans as many small rows as
 * it is tall, and the grid fills the free space with the next card that fits ("dense"). A
 * short card goes up beside a tall one instead of leaving a gap under the shorter one.
 */
function useMasonry(ref: RefObject<HTMLElement | null>, on: boolean) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const items = () => [...el.children] as HTMLElement[];
    if (!on) {
      el.classList.remove("masonry");
      for (const c of items()) c.style.gridRowEnd = "";
      return;
    }
    el.classList.add("masonry");
    // Heights are all read first and the rows written after: a write between two reads
    // would make the browser lay the page out again for every card.
    const place = (cards: HTMLElement[], heights: number[]) => {
      cards.forEach((c, i) => {
        c.style.gridRowEnd = `span ${Math.max(1, Math.ceil((heights[i]! + GAP) / ROW))}`;
      });
    };
    const sizes = new ResizeObserver((entries) => {
      // The observer has the border box measured already, so it costs no layout.
      place(
        entries.map((e) => e.target as HTMLElement),
        entries.map((e) => e.borderBoxSize?.[0]?.blockSize ?? e.target.getBoundingClientRect().height),
      );
    });
    const watch = () => {
      sizes.disconnect();
      const cards = items();
      place(
        cards,
        cards.map((c) => c.getBoundingClientRect().height),
      );
      for (const c of cards) sizes.observe(c);
    };
    watch();
    // Cards come and go with the section and with loading.
    const list = new MutationObserver(watch);
    list.observe(el, { childList: true });
    return () => {
      sizes.disconnect();
      list.disconnect();
      el.classList.remove("masonry");
      for (const c of items()) c.style.gridRowEnd = "";
    };
  }, [ref, on]);
}

/** The width of an element, followed as it changes; 0 before the first measure. */
function useWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.getBoundingClientRect().width);
    const ro = new ResizeObserver(([e]) => setWidth(e?.contentRect.width ?? 0));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}

/**
 * An editor and its live preview: side by side once both fit (the preview follows the
 * scroll), otherwise the preview opens from a button above the editor instead of ending up
 * under it. `preview(bare)` draws it without its own card inside the side sheet.
 */
export function WithPreview({ preview, title, children, min = 880 }: { preview: (bare: boolean) => ReactNode; title: string; children: ReactNode; min?: number }) {
  const ref = useRef<HTMLDivElement>(null);
  const width = useWidth(ref);
  const [open, setOpen] = useState(false);
  const side = width >= min;
  return (
    <div ref={ref} className={side ? "with-preview" : "flex flex-col gap-4"}>
      <div className="flex min-w-0 flex-col gap-4">
        {width > 0 && !side ? (
          <div className="flex justify-end">
            <Button size="sm" onClick={() => setOpen(true)} aria-haspopup="dialog">
              <Eye size={16} aria-hidden /> {t("common.showPreview")}
            </Button>
          </div>
        ) : null}
        {children}
      </div>
      {side ? <aside aria-label={title}>{preview(false)}</aside> : null}
      {!side ? (
        <Drawer open={open} onOpenChange={setOpen} title={title}>
          <div className="pt-4">{open ? preview(true) : null}</div>
        </Drawer>
      ) : null}
    </div>
  );
}

/** Save (and, with a draft, reset) of a card's form: always at the card's bottom right. */
export function FormActions({ dirty, saving, onReset, disabled, label, note, children }: { dirty?: boolean; saving?: boolean; onReset?: () => void; disabled?: boolean; label?: string; note?: ReactNode; children?: ReactNode }) {
  return (
    <div className="form-actions">
      {note ? <span className="form-actions-note">{note}</span> : null}
      {children}
      {onReset && dirty ? (
        <Button variant="ghost" onClick={onReset} disabled={saving}>
          {t("common.discard")}
        </Button>
      ) : null}
      <Button type="submit" variant="primary" loading={saving} disabled={disabled ?? dirty === false}>
        {label ?? t("common.save")}
      </Button>
    </div>
  );
}

/** The bar of a page's unsaved draft, under the content and kept in view while it scrolls. */
export function SaveBar({ dirty, saving, onSave, onReset, label }: { dirty: boolean; saving?: boolean; onSave: () => void; onReset: () => void; label?: string }) {
  const reduce = useReducedMotion();
  return (
    <AnimatePresence>
      {dirty ? (
        <motion.div
          className="savebar glass-strong"
          role="region"
          aria-label={label ?? t("common.unsaved")}
          initial={reduce ? { opacity: 0 } : { opacity: 0, y: 24 }}
          animate={reduce ? { opacity: 1 } : { opacity: 1, y: 0 }}
          exit={reduce ? { opacity: 0 } : { opacity: 0, y: 24 }}
          transition={{ type: "spring", stiffness: 420, damping: 32 }}
        >
          <span className="savebar-text">{label ?? t("common.unsaved")}</span>
          <Button variant="ghost" size="sm" onClick={onReset} disabled={saving}>
            {t("common.discard")}
          </Button>
          <Button variant="primary" size="sm" loading={saving} onClick={onSave}>
            {t("common.save")}
          </Button>
        </motion.div>
      ) : null}
    </AnimatePresence>
  );
}

/**
 * What most people never change, folded under a title. It opens by itself when `open` turns
 * true (a field inside has an error), and the user can open and close it freely.
 */
export function Disclosure({ title, sub, open, children }: { title: string; sub?: ReactNode; open?: boolean; children: ReactNode }) {
  const [shown, setShown] = useState(!!open);
  const [forced, setForced] = useState(!!open);
  if (!!open !== forced) {
    setForced(!!open);
    if (open) setShown(true);
  }
  return (
    <details className="disclosure" open={shown} onToggle={(e) => setShown(e.currentTarget.open)}>
      <summary>
        <ChevronRight size={16} aria-hidden />
        <span className="min-w-0">
          <span className="disclosure-title block">{title}</span>
          {sub ? <span className="disclosure-sub block">{sub}</span> : null}
        </span>
      </summary>
      <div className="disclosure-body">{children}</div>
    </details>
  );
}
