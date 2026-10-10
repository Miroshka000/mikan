import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowDownToLine, Cpu, Globe, LayoutTemplate, Link2, Route, ShieldCheck } from "lucide-react";
import { useSettings } from "../../../api/hooks";
import { SectionNav } from "../../../components/layout";
import { QueryBoundary } from "../../../components/query";
import { ThemeCard } from "../../../components/theme";
import { PageHeader, Skeleton } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { SETTINGS_PARTS, SETTINGS_TABS, type SettingsSearch } from "../../search";
import { AutoCard, LanguageCard, ServiceCard, UpdatesCard } from "./general";
import { ServerCheckCard } from "../node-check";
import { GoalsCard } from "./goals";
import { ImportCard, LegacyLinksCard } from "./import";
import { RoutingSection } from "./routing";
import { AccessCard, ApiCard, CertificateCard, PasswordCard, SessionsCard, TwoFactorCard } from "./security";
import { AppsCard, DevicesCard, SubPortCard, SubscriptionCard } from "./subscription";
import { HappCard } from "./happ";
import { SubPageSettings, type SubPagePart } from "./sub-page";

type Tab = SettingsSearch["tab"];
type RoutingPart = "simple" | "yaml";

const ICONS = { general: Globe, subscription: Link2, page: LayoutTemplate, routing: Route, security: ShieldCheck, system: Cpu, import: ArrowDownToLine } as const;

const PART_LABELS: Record<string, Key> = {
  look: "settings.page.tab.look",
  brand: "settings.page.tab.brand",
  blocks: "settings.page.tab.blocks",
  apps: "settings.page.tab.apps",
  docs: "settings.page.tab.docs",
  css: "settings.page.tab.css",
};

/**
 * Settings in sections listed beside the content (a select on phones); the section and its
 * part are in the URL, so a link opens them. Forms keep one readable width; the editors with
 * a live preview take the whole column.
 */
export function SettingsPage() {
  const settings = useSettings();
  const { tab, part } = useSearch({ from: "/_app/settings" });
  const navigate = useNavigate({ from: "/settings" });
  const go = (next: Tab, nextPart?: string) => void navigate({ search: { tab: next, part: nextPart }, replace: true });
  // The routing opens where it is in use: the own YAML once there is one.
  const routingPart: RoutingPart = part === "simple" || part === "yaml" ? part : settings.data?.sub_template.trim() ? "yaml" : "simple";
  const pagePart: SubPagePart = (part as SubPagePart | undefined) ?? "look";
  const shownPart = tab === "page" ? pagePart : undefined;
  const wide = tab === "page" || tab === "routing";
  return (
    <>
      <PageHeader title={t("nav.settings")} sub={t("settings.subtitle")} />
      <SectionNav
        label={t("settings.sections")}
        sections={SETTINGS_TABS.map((id) => ({
          id,
          label: t(`settings.tabs.${id}`),
          icon: ICONS[id],
          parts: id in SETTINGS_PARTS ? SETTINGS_PARTS[id as keyof typeof SETTINGS_PARTS].map((p) => ({ id: p, label: t(PART_LABELS[p]!) })) : undefined,
        }))}
        value={tab}
        part={shownPart}
        onChange={go}
        narrow={!wide}
      >
        {tab === "page" ? (
          // The editor loads its own data and keeps its own draft.
          <SubPageSettings section={pagePart} onSection={(p) => go("page", p)} />
        ) : (
          <QueryBoundary query={settings} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
            {(s) =>
              tab === "general" ? (
                <>
                  <ServiceCard s={s} />
                  <LanguageCard s={s} />
                  <ThemeCard />
                </>
              ) : tab === "subscription" ? (
                <>
                  <DevicesCard s={s} />
                  <AppsCard s={s} />
                  <HappCard s={s} />
                  <SubscriptionCard s={s} />
                  <SubPortCard s={s} />
                </>
              ) : tab === "routing" ? (
                <RoutingSection s={s} view={routingPart} onView={(v) => go("routing", v)} />
              ) : tab === "security" ? (
                <>
                  <PasswordCard />
                  <TwoFactorCard />
                  <SessionsCard />
                  <AccessCard s={s} />
                  <CertificateCard s={s} />
                  <ApiCard />
                </>
              ) : tab === "system" ? (
                <>
                  <UpdatesCard />
                  <ServerCheckCard />
                  <GoalsCard />
                  <AutoCard s={s} />
                </>
              ) : (
                <>
                  <ImportCard />
                  <LegacyLinksCard />
                </>
              )
            }
          </QueryBoundary>
        )}
      </SectionNav>
    </>
  );
}
