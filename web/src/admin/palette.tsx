// The command palette (Ctrl+K, ⌘K on a Mac): a client by name or id, a node, a section of the
// menu and the few actions the panel has a button for, from anywhere in it. Clients are found
// by the list's own search on the server; the nodes, the sections and the actions are few and
// already here, so they are filtered in the page.
import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ArrowUpCircle, Network, Plus, Search, UserPlus, UserRound, type LucideIcon } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { useEffect, useId, useState, type Dispatch, type KeyboardEvent as ReactKeyboardEvent, type SetStateAction } from "react";
import { api, unwrap } from "../api/client";
import { qk, useNodes, useUpdates } from "../api/hooks";
import { useSupportMode } from "../api/support";
import { t, type Key } from "../i18n";
import { useDebounced } from "../lib/debounce";
import { nodeLabel } from "../lib/node-label";
import { isPaletteKey, matches, onMac, step, userIdOf } from "../lib/palette";
import { NAV } from "./nav";

/** How many clients the search shows: the palette is a way to one of them, not the list. */
const USERS_SHOWN = 8;

const GROUPS = ["users", "nodes", "actions", "sections"] as const;
type Group = (typeof GROUPS)[number];
type Item = { id: string; group: Group; icon: LucideIcon; label: string; hint?: string; run: () => void };

/** The palette's key as the sidebar shows it and as aria-keyshortcuts names it. */
export function paletteKeyLabel(): { text: string; aria: string } {
  return onMac() ? { text: "⌘K", aria: "Meta+K" } : { text: "Ctrl K", aria: "Control+K" };
}

/**
 * Opens and closes the palette by its key anywhere in the panel, except in the terminal: there
 * Ctrl+K is the shell's own (cut to the end of the line).
 */
export function usePaletteKey(setOpen: Dispatch<SetStateAction<boolean>>) {
  useEffect(() => {
    const mac = onMac();
    const onKey = (e: KeyboardEvent) => {
      if (e.repeat || !isPaletteKey(e, mac)) return;
      if (e.target instanceof Element && e.target.closest(".xterm")) return;
      e.preventDefault();
      setOpen((o) => !o);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setOpen]);
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <AnimatePresence>
        {open ? (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="scrim" style={{ zIndex: 65 }} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} />
            </Dialog.Overlay>
            <Dialog.Content asChild forceMount aria-describedby={undefined}>
              <motion.div className="palette glass-strong" initial={{ opacity: 0, scale: 0.98 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.98 }} transition={{ duration: 0.15 }}>
                <Dialog.Title className="sr-only">{t("palette.title")}</Dialog.Title>
                <PaletteBody close={() => onOpenChange(false)} />
              </motion.div>
            </Dialog.Content>
          </Dialog.Portal>
        ) : null}
      </AnimatePresence>
    </Dialog.Root>
  );
}

/** What the palette holds while it is open: a new search each time it opens. */
function PaletteBody({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const operator = useSupportMode();
  // An operator the admin did not let see the subscribers may not search them (the API says
  // 403): a client is found by its id only. One who only looks creates nothing, and a node is
  // added by a session of the admin's or in the extended mode.
  const hidesPeople = !!operator && !operator.show_people;
  const writes = !operator || operator.level !== "view";
  const addsNodes = writes && (!operator || operator.extended);

  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const query = q.trim();
  const asked = useDebounced(query, 200);
  const users = useQuery({
    queryKey: [...qk.users, "palette", asked],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users", { params: { query: { state: "all", q: asked, hidden: "show", limit: USERS_SHOWN } }, signal })),
    enabled: asked !== "" && !hidesPeople,
    staleTime: 10_000,
  });
  const nodes = useNodes();
  const updates = useUpdates();

  const go = (to: () => unknown) => () => {
    close();
    void to();
  };
  const items: Item[] = [];
  if (query) {
    const typedId = userIdOf(query);
    const found = asked === query && !hidesPeople ? (users.data?.items ?? []) : [];
    if (typedId && !found.some((u) => u.id === typedId)) {
      items.push({ id: `user-id-${typedId}`, group: "users", icon: UserRound, label: t("palette.userById", { id: typedId }), run: go(() => navigate({ to: "/users", search: { state: "all", q: "", user: typedId } })) });
    }
    for (const u of found) {
      items.push({ id: `user-${u.id}`, group: "users", icon: UserRound, label: u.name, hint: `#${u.id} · ${t(`states.${u.state}`)}`, run: go(() => navigate({ to: "/users", search: { state: "all", q: "", user: u.id } })) });
    }
    for (const n of nodes.data ?? []) {
      const label = nodeLabel(n);
      if (!matches(`${label} ${n.public_name} ${n.server_host} ${n.domain} #${n.id}`, query)) continue;
      items.push({ id: `node-${n.id}`, group: "nodes", icon: Network, label, hint: n.domain || n.server_host, run: go(() => navigate({ to: "/nodes", search: { node: n.id } })) });
    }
  }
  const actions: Item[] = [];
  if (writes) actions.push({ id: "new-user", group: "actions", icon: UserPlus, label: t("palette.newUser"), run: go(() => navigate({ to: "/users", search: { state: "all", q: "", create: true } })) });
  if (addsNodes) actions.push({ id: "add-node", group: "actions", icon: Plus, label: t("palette.addNode"), run: go(() => navigate({ to: "/nodes", search: { add: true } })) });
  if (updates.data?.available) {
    actions.push({ id: "update", group: "actions", icon: ArrowUpCircle, label: t("shell.update", { v: updates.data.latest }), hint: t("shell.updateHint"), run: go(() => navigate({ to: "/settings", search: { tab: "system" }, hash: "updates" })) });
  }
  items.push(...actions.filter((a) => matches(a.label, query)));
  // A section is found by its name in the language in use and by its English key ("users").
  for (const n of NAV) {
    const label = t(`nav.${n.key}`);
    if (matches(`${label} ${n.key}`, query)) items.push({ id: `section-${n.key}`, group: "sections", icon: n.icon, label, run: go(() => navigate({ to: n.to })) });
  }

  const cur = Math.min(active, Math.max(items.length - 1, 0));
  const listId = useId();
  const optionId = (i: number) => `${listId}-${i}`;
  useEffect(() => {
    document.getElementById(`${listId}-${cur}`)?.scrollIntoView({ block: "nearest" });
  }, [listId, cur]);

  const onKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setActive(step(cur, items.length, e.key === "ArrowDown" ? 1 : -1));
    } else if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      e.preventDefault();
      items[cur]?.run();
    }
  };

  const searching = query !== "" && !hidesPeople && (asked !== query || users.isFetching);
  let note: Key | undefined;
  if (query && hidesPeople && !userIdOf(query)) note = "palette.peopleHidden";
  else if (query && users.isError) note = "palette.searchFailed";
  return (
    <>
      <div className="palette-input">
        <Search size={18} aria-hidden className="shrink-0 text-[var(--ink-500)]" />
        <input
          role="combobox"
          aria-expanded={items.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={items.length ? optionId(cur) : undefined}
          aria-label={t("palette.title")}
          placeholder={hidesPeople ? t("palette.placeholderNoPeople") : t("palette.placeholder")}
          autoComplete="off"
          spellCheck={false}
          maxLength={100}
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      <div className="palette-list">
        <div id={listId} role="listbox" aria-label={t("palette.title")}>
          {GROUPS.map((g) => {
            const of = items.map((it, i) => [it, i] as const).filter(([it]) => it.group === g);
            if (!of.length) return null;
            return (
              <div key={g} role="group" aria-labelledby={`${listId}-${g}`}>
                <div className="menu-label" id={`${listId}-${g}`}>
                  {t(`palette.group.${g}`)}
                </div>
                {of.map(([it, i]) => (
                  <div
                    key={it.id}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === cur}
                    className="palette-item"
                    // The focus stays in the field: the arrows and Enter go on working after a click.
                    onMouseDown={(e) => e.preventDefault()}
                    onMouseMove={() => i !== cur && setActive(i)}
                    onClick={it.run}
                  >
                    <it.icon size={16} aria-hidden className="shrink-0" />
                    <span className="truncate">{it.label}</span>
                    {it.hint ? <span className="palette-hint truncate">{it.hint}</span> : null}
                  </div>
                ))}
              </div>
            );
          })}
        </div>
        {/* Outside the listbox: a listbox holds only options and their groups. */}
        {note ? <p className="palette-note">{t(note)}</p> : null}
        {!items.length && !note && !searching ? <p className="palette-note">{t("palette.nothing")}</p> : null}
      </div>
      <p className="sr-only" role="status">
        {searching ? t("palette.searching") : t("palette.count", { n: items.length })}
      </p>
      <div className="palette-foot" aria-hidden>
        <span>
          <kbd>↑</kbd> <kbd>↓</kbd> {t("palette.hintMove")}
        </span>
        <span>
          <kbd>Enter</kbd> {t("palette.hintOpen")}
        </span>
        <span>
          <kbd>Esc</kbd> {t("palette.hintClose")}
        </span>
      </div>
    </>
  );
}
