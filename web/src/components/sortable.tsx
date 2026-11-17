import clsx from "clsx";
import { GripVertical } from "lucide-react";
import { Reorder, useDragControls } from "motion/react";
import { useId, useRef, useState, type FocusEvent, type KeyboardEvent, type ReactNode } from "react";
import { t } from "../i18n";

/**
 * A list the admin puts in order: an item is dragged by its handle (mouse, finger), or with
 * the keyboard, where the list is one stop of Tab: the arrows go from item to item, Space
 * picks one up, the arrows move it, Space drops it, Escape puts it back (motion's Reorder).
 * A move made with the keyboard is read out. The order is the caller's draft: onReorder
 * gets every step of a drag.
 */
export function SortableList<T extends number | string>({
  values,
  onReorder,
  label,
  nameOf,
  disabled,
  className,
  children,
}: {
  values: T[];
  onReorder: (next: T[]) => void;
  /** What the list is, for screen readers. */
  label: string;
  /** An item's name, for screen readers: what tells it from the others. */
  nameOf: (v: T) => string;
  /** The order is on its way to the panel: nothing moves meanwhile. */
  disabled?: boolean;
  className?: string;
  children: (v: T, index: number) => ReactNode;
}) {
  const keysId = useId();
  const [said, setSaid] = useState("");
  // The item picked up with the keyboard (its key), as motion has it: Space picks up and
  // drops, Escape and focus leaving the item drop.
  const [grabbed, setGrabbed] = useState<string | null>(null);
  // The order when it was picked up, for Escape to put back.
  const from = useRef<T[] | null>(null);
  // A drag reads nothing out: whoever drags sees where the item goes.
  const byKeys = useRef(false);
  const total = values.length;
  const say = (order: T[], moved: T) => setSaid(t("reorder.moved", { name: nameOf(moved), n: order.indexOf(moved) + 1, total }));
  // The keys motion takes: on an item itself, without Alt, Ctrl or Cmd.
  const itemKey = (e: KeyboardEvent | FocusEvent) => {
    const el = e.target as HTMLElement;
    return el.classList?.contains("sortable-item") ? (el.dataset.key ?? null) : null;
  };
  return (
    <>
      <p id={keysId} className="sr-only">
        {t("reorder.keys")}
      </p>
      <Reorder.Group
        as="ol"
        axis="y"
        values={values}
        aria-label={label}
        className={clsx("sortable", className)}
        // While an item is up, Escape puts it back and does not close the window around.
        data-local-escape={grabbed !== null || undefined}
        onKeyDownCapture={(e: KeyboardEvent) => {
          const key = itemKey(e);
          byKeys.current = key !== null && !e.altKey && !e.ctrlKey && !e.metaKey && (e.key.startsWith("Arrow") || e.key === "Escape" || e.key === " ");
          if (!byKeys.current || disabled) return;
          if (e.key === " " && !e.repeat) {
            from.current = grabbed === key ? null : values;
            setGrabbed(grabbed === key ? null : key);
          } else if (e.key === "Escape" && grabbed !== null) {
            // A window around takes Escape first and marks it handled (it stays open for it), and
            // motion skips a handled key: the order goes back here, and leaving the item drops it.
            const back = from.current;
            const item = values.find((v) => String(v) === grabbed);
            if (e.defaultPrevented && back && item !== undefined) {
              onReorder(back);
              say(back, item);
              const el = e.target as HTMLElement;
              el.blur();
              el.focus({ preventScroll: true });
              requestAnimationFrame(() => keepInSight(el));
            }
            from.current = null;
            setGrabbed(null);
          }
        }}
        onBlurCapture={(e: FocusEvent) => {
          if (itemKey(e) === grabbed) {
            from.current = null;
            setGrabbed(null);
          }
        }}
        onPointerDownCapture={() => {
          byKeys.current = false;
        }}
        onReorder={(next: T[], moved: T) => {
          if (disabled) return;
          onReorder(next);
          if (!byKeys.current) return;
          say(next, moved);
          // The item moved with the keys holds the focus: it stays in sight in its new place.
          requestAnimationFrame(() => {
            const el = document.activeElement;
            if (el instanceof HTMLElement && el.dataset.key === String(moved)) keepInSight(el);
          });
        }}
      >
        {values.map((v, i) => (
          <SortableItem key={v} value={v} label={t("reorder.item", { name: nameOf(v), n: i + 1, total })} describedBy={keysId} grabbed={grabbed === String(v)} disabled={disabled}>
            {children(v, i)}
          </SortableItem>
        ))}
      </Reorder.Group>
      <p className="sr-only" aria-live="polite" aria-atomic="true">
        {said}
      </p>
    </>
  );
}

/**
 * Scrolls the box el is listed in (or the page) so that el's new place is in sight: its box
 * less the transform motion slides it from the old place by.
 */
function keepInSight(el: HTMLElement) {
  const tf = getComputedStyle(el).transform;
  const shift = tf && tf !== "none" ? new DOMMatrixReadOnly(tf).m42 : 0;
  const r = el.getBoundingClientRect();
  const top = r.top - shift;
  const bottom = r.bottom - shift;
  let box = el.parentElement;
  while (box && !/(auto|scroll)/.test(getComputedStyle(box).overflowY)) box = box.parentElement;
  const view = box ? box.getBoundingClientRect() : { top: 0, bottom: window.innerHeight };
  const gap = 8;
  const by = top < view.top + gap ? top - view.top - gap : bottom > view.bottom - gap ? bottom - view.bottom + gap : 0;
  if (by === 0) return;
  if (box) box.scrollTop += by;
  else window.scrollBy(0, by);
}

function SortableItem<T extends number | string>({
  value,
  label,
  describedBy,
  grabbed,
  disabled,
  children,
}: {
  value: T;
  label: string;
  describedBy: string;
  grabbed: boolean;
  disabled?: boolean;
  children: ReactNode;
}) {
  const controls = useDragControls();
  const [dragging, setDragging] = useState(false);
  return (
    <Reorder.Item
      as="li"
      value={value}
      drag={disabled ? false : "y"}
      dragListener={false}
      dragControls={controls}
      className="sortable-item"
      data-key={String(value)}
      data-lifted={dragging || grabbed || undefined}
      aria-label={label}
      aria-describedby={describedBy}
      aria-disabled={disabled || undefined}
      whileDrag={{ scale: 1.01 }}
      onDragStart={() => setDragging(true)}
      onDragEnd={() => setDragging(false)}
    >
      {/* The handle starts a drag of the mouse or the finger; the keyboard moves the item itself. */}
      <span
        className="sortable-handle"
        aria-hidden
        onPointerDown={(e) => {
          if (disabled) return;
          e.preventDefault();
          controls.start(e);
        }}
      >
        <GripVertical size={18} />
      </span>
      <div className="min-w-0 flex-1">{children}</div>
    </Reorder.Item>
  );
}
