import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import * as Menu from "@radix-ui/react-dropdown-menu";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { ArrowUpCircle, LogOut, MoreHorizontal, PanelLeftClose, PanelLeftOpen, Search } from "lucide-react";
import { useState } from "react";
import { api, ApiError, errorText, unwrap } from "../api/client";
import { meQuery, useNode, useOverview, useUpdates } from "../api/hooks";
import { useSupportMode } from "../api/support";
import { Logo } from "../components/atmosphere";
import { LangSwitch } from "../components/lang";
import { useToast } from "../components/toast";
import { Avatar, Bar, Pill } from "../components/ui";
import { t, useLocale } from "../i18n";
import { num, uptime } from "../lib/format";
import { NAV } from "./nav";
import { CommandPalette, paletteKeyLabel, usePaletteKey } from "./palette";
import { SupportBar } from "./support-bar";

// The sidebar folded to its icons on a wide screen, as the admin left it; per browser.
const FOLD_KEY = "mikan.sidebar";

function readFolded(): boolean {
  try {
    return localStorage.getItem(FOLD_KEY) === "folded";
  } catch {
    return false;
  }
}

export function Shell() {
  useLocale(); // the sidebar and the bar read their texts at render time
  const overview = useOverview({ poll: false });
  const [folded, setFolded] = useState(readFolded);
  const fold = () =>
    setFolded((f) => {
      try {
        if (f) localStorage.removeItem(FOLD_KEY);
        else localStorage.setItem(FOLD_KEY, "folded");
      } catch {
        // private mode: folded for this visit only
      }
      return !f;
    });
  const foldLabel = folded ? t("shell.unfold") : t("shell.fold");
  const [palette, setPalette] = useState(false);
  usePaletteKey(() => setPalette((o) => !o));
  const openPalette = () => setPalette(true);
  return (
    <>
      <SupportBar />
      <div className="app" data-folded={folded || undefined}>
        <aside className="sidebar glass" aria-label={t("shell.sidebar")}>
          <div className="brand">
            <Logo />
            <span className="brand-name">mikan</span>
            <button type="button" className="icon-btn fold-btn" onClick={fold} aria-label={foldLabel} title={foldLabel}>
              {folded ? <PanelLeftOpen size={18} aria-hidden /> : <PanelLeftClose size={18} aria-hidden />}
            </button>
          </div>
          <nav className="nav" aria-label={t("shell.sections")}>
            <button type="button" className="nav-item" onClick={openPalette} aria-haspopup="dialog" aria-keyshortcuts={paletteKeyLabel().aria} title={`${t("palette.open")} (${paletteKeyLabel().text})`}>
              <Search size={18} aria-hidden />
              <span className="nav-label">{t("palette.open")}</span>
              <kbd className="nav-count">{paletteKeyLabel().text}</kbd>
            </button>
            {NAV.map((n) => (
              <Link key={n.to} to={n.to} className="nav-item" activeProps={{ className: "active", "aria-current": "page" }} activeOptions={{ exact: n.to === "/" }} title={t(`nav.${n.key}`)}>
                <n.icon size={18} aria-hidden />
                <span className="nav-label">{t(`nav.${n.key}`)}</span>
                {n.to === "/users" && overview.data ? <span className="nav-count num">{num(overview.data.users_total)}</span> : null}
              </Link>
            ))}
          </nav>
          <div className="side-foot">
            <UpdateChip />
            <NodeCard />
            <AdminRow />
            <LangSwitch className="self-start" />
          </div>
        </aside>
        <main className="main">
          <Outlet />
        </main>
      </div>
      <MobileNav onSearch={openPalette} />
      <CommandPalette open={palette} onOpenChange={setPalette} />
    </>
  );
}

// The phone's bottom bar has room for four sections; the rest sit under "More".
const MOBILE_MAIN = 4;

// A section is current on its own page and on the pages below it (/addons/telegram).
const isIn = (path: string, to: string) => path.endsWith(to) || path.includes(`${to}/`);

function MobileNav({ onSearch }: { onSearch: () => void }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const more = NAV.slice(MOBILE_MAIN);
  const inMore = more.some((n) => isIn(path, n.to));
  return (
    <nav className="mnav glass" aria-label={t("shell.sections")}>
      {NAV.slice(0, MOBILE_MAIN).map((n) => (
        <Link key={n.to} to={n.to} activeProps={{ className: "active", "aria-current": "page" }} activeOptions={{ exact: n.to === "/" }}>
          <n.icon size={20} aria-hidden />
          <span>{t(`navShort.${n.key}`)}</span>
        </Link>
      ))}
      <Menu.Root>
        <Menu.Trigger asChild>
          <button type="button" className={inMore ? "active" : undefined} aria-label={t("shell.more")}>
            <MoreHorizontal size={20} aria-hidden />
            <span>{t("shell.more")}</span>
          </button>
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content className="menu glass-strong" side="top" align="end" sideOffset={12}>
            {more.map((n) => (
              <Menu.Item key={n.to} className="menu-item" onSelect={() => void navigate({ to: n.to })} aria-current={isIn(path, n.to) ? "page" : undefined}>
                <n.icon size={16} aria-hidden /> {t(`nav.${n.key}`)}
              </Menu.Item>
            ))}
            <Menu.Separator className="menu-sep" />
            {/* Opened once the menu has closed and given the focus back: the palette takes it then. */}
            <Menu.Item className="menu-item" onSelect={() => window.setTimeout(onSearch)}>
              <Search size={16} aria-hidden /> {t("palette.open")}
            </Menu.Item>
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
    </nav>
  );
}

/** A new release: the way to the settings' Updates card. */
function UpdateChip() {
  const u = useUpdates();
  if (!u.data?.available) return null;
  return (
    <Link to="/settings" search={{ tab: "system" }} hash="updates" className="update-chip" title={t("shell.updateHint")}>
      <ArrowUpCircle size={16} aria-hidden />
      <span className="update-label truncate">{t("shell.update", { v: u.data.latest })}</span>
    </Link>
  );
}

function NodeCard() {
  const node = useNode();
  const n = node.data;
  if (!n) {
    return (
      <div className="node-card" aria-busy>
        <span className="sk" style={{ width: "60%" }} />
        <span className="sk mt-3" style={{ width: "90%" }} />
      </div>
    );
  }
  const memPct = n.system.mem_total ? (n.system.mem_used / n.system.mem_total) * 100 : 0;
  return (
    <div className="node-card">
      <div className="node-top">
        <span className="node-name">{t("shell.server")}</span>
        {n.ok ? <Pill tone="ok">{t("shell.running")}</Pill> : <Pill tone="bad">{t("shell.offline")}</Pill>}
      </div>
      <div className="node-sub">{n.ok ? `${n.core}${n.started_at ? ` · ${uptime(n.started_at)}` : ""}` : t("shell.nodeDown")}</div>
      {n.ok ? (
        <div className="node-bars">
          <span>CPU</span>
          <Bar pct={n.system.cpu_percent} label="CPU" />
          <span className="num">{Math.round(n.system.cpu_percent)}%</span>
          <span>RAM</span>
          <Bar pct={memPct} label="RAM" />
          <span className="num">{Math.round(memPct)}%</span>
        </div>
      ) : null}
    </div>
  );
}

function AdminRow() {
  // A support operator has no account of their own to show (the admin's data is closed to them).
  const operator = useSupportMode();
  const me = useQuery({ ...meQuery, enabled: !operator });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const logout = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/logout")),
    onSuccess: () => {
      qc.clear();
      void navigate({ to: "/login", search: {} });
    },
    // A failed sign-out leaves the session alive on the server: the admin stays here with the reason
    // instead of a login form that would be untrue. A 401 is the session already gone, and the unauthorized event moves them to the login.
    onError: (e) => {
      if (!(e instanceof ApiError && e.status === 401)) toast.error(errorText(e));
    },
  });
  if (operator) {
    // No sign-out for an operator: the access ends when the admin revokes it or its time is up.
    return (
      <div className="admin-row">
        <Avatar name={t("support.operator")} seed={2} size="sm" />
        <div className="who-wrap min-w-0">
          <div className="truncate text-[13px] font-medium">{t("support.operator")}</div>
          <div className="text-xs text-[var(--ink-500)]">{t(`support.level.${operator.level}`)}</div>
        </div>
      </div>
    );
  }
  const name = me.data?.admin.username ?? "…";
  return (
    <div className="admin-row">
      <Avatar name={name} seed={4} size="sm" />
      <div className="who-wrap min-w-0">
        <div className="truncate text-[13px] font-medium">{name}</div>
        <div className="text-xs text-[var(--ink-500)]">{me.data?.admin.totp_enabled ? t("shell.twoFactorOn") : t("shell.owner")}</div>
      </div>
      <button type="button" className="icon-btn logout ml-auto" aria-label={t("shell.logout")} onClick={() => logout.mutate()} disabled={logout.isPending}>
        <LogOut size={18} />
      </button>
    </div>
  );
}
