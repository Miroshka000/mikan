import * as Menu from "@radix-ui/react-dropdown-menu";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, MoreHorizontal, type LucideIcon } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";

// The pieces of the addons page every kind of row is made of: the row, its menu, a group of
// rows, and the way back from an addon's own page.

export type Tone = "ok" | "warn" | "bad" | "off";
export type State = { tone: Tone; text: string };

/**
 * One addon: its icon, its name with its marks and a line of what it is, its state, and its
 * actions. On a phone the state and the actions go under the text.
 */
export function Row({
  icon: Icon,
  name,
  marks,
  sub,
  error,
  state,
  actions,
  menu,
}: {
  icon: LucideIcon;
  name: string;
  marks?: ReactNode;
  sub: ReactNode;
  error?: ReactNode;
  state: State | undefined;
  actions?: ReactNode;
  menu?: ReactNode;
}) {
  return (
    <li className="addon-row">
      <span className="addon-row-icon" aria-hidden>
        <Icon size={20} />
      </span>
      <div className="min-w-0">
        <div className="addon-row-title">
          <span className="min-w-0 break-words">{name}</span>
          {marks}
        </div>
        <div className="addon-row-sub">{sub}</div>
        {error ? (
          <div className="addon-row-err" role="alert">
            {error}
          </div>
        ) : null}
      </div>
      <div className="addon-row-side">
        <span className="addon-row-state">{state ? <Pill tone={state.tone}>{state.text}</Pill> : <Skeleton style={{ height: 24, width: 88, borderRadius: 999 }} />}</span>
        <span className="addon-row-actions">
          {actions}
          {/* Rows without a menu keep its place, so the buttons stand in one column. */}
          {menu ?? <span className="w-9 shrink-0" aria-hidden />}
        </span>
      </div>
    </li>
  );
}

export function RowMenu({ name, children }: { name: string; children: ReactNode }) {
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <button type="button" className="icon-btn shrink-0" aria-label={t("addons.moreActions", { name })}>
          <MoreHorizontal size={18} aria-hidden />
        </button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
          {children}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}

export function Item({ icon: Icon, onSelect, danger, children }: { icon: LucideIcon; onSelect: () => void; danger?: boolean; children: ReactNode }) {
  return (
    <Menu.Item className={danger ? "menu-item danger" : "menu-item"} onSelect={onSelect}>
      <Icon size={16} aria-hidden /> {children}
    </Menu.Item>
  );
}

/** A group of rows under its label, in one bordered list. */
export function Group({ title, i, children }: { title: string; i: number; children: ReactNode }) {
  return (
    <section className="reveal" style={{ "--i": i } as CSSProperties} aria-label={title}>
      <h2 className="addon-group-title">{title}</h2>
      <ul className="addon-list card glass">{children}</ul>
    </section>
  );
}

export function BackToAddons() {
  return (
    <Link to="/addons" className="btn btn-glass">
      <ArrowLeft size={16} aria-hidden />
      {t("addons.back")}
    </Link>
  );
}
