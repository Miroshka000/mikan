import { Activity, LayoutDashboard, Network, Puzzle, Server, SlidersHorizontal, Tag, Users, Wallet } from "lucide-react";

/** The sections of the menu: the sidebar, the phone's bar and the command palette list them. */
export const NAV = [
  { to: "/", key: "overview", icon: LayoutDashboard },
  { to: "/users", key: "users", icon: Users },
  { to: "/tariffs", key: "tariffs", icon: Tag },
  { to: "/inbounds", key: "inbounds", icon: Server },
  { to: "/nodes", key: "nodes", icon: Network },
  { to: "/payments", key: "payments", icon: Wallet },
  { to: "/addons", key: "addons", icon: Puzzle },
  { to: "/diagnostics", key: "diagnostics", icon: Activity },
  { to: "/settings", key: "settings", icon: SlidersHorizontal },
] as const;
