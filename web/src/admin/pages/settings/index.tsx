import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowDownToLine, Cpu, Globe, LayoutTemplate, LifeBuoy, Link2, Route, ShieldCheck } from "lucide-react";
import { useSettings } from "../../../api/hooks";
import { useSupportMode } from "../../../api/support";
import { SectionNav } from "../../../components/layout";
import { QueryBoundary } from "../../../components/query";
import { ThemeCard } from "../../../components/theme";
import { PageHeader, Skeleton } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { SETTINGS_PARTS, SETTINGS_TABS, type SettingsSearch } from "../../search";
import { ClosedForSupport } from "../../support-closed";
import { AppCard } from "./app";
import { AuditCard } from "./audit";
import { AutoCard, LanguageCard, ServiceCard, UpdatesCard } from "./general";
import { ServerCheckCard } from "../node-check";
import { GoalsCard } from "./goals";
import { ImportCard, LegacyLinksCard } from "./import";
import { ROUTING_PARTS, RoutingSection, type RoutingPart } from "./routing";
import { AccessCard, ApiCard, CertificateCard, PasskeysCard, PasswordCard, SessionsCard, TwoFactorCard } from "./security";
import { SupportCard } from "./support";
import { AppsCard, DevicesCard, InactiveCard, SubPortCard, SubscriptionCard, XrayJSONCard } from "./subscription";
import { SubDomainCard } from "./sub-domain";
import { SiteCard } from "./site";
import { HappCard } from "./happ";
import { SubPageSettings, type SubPagePart } from "./sub-page";
import { WebhooksCard } from "./webhooks-link";
import { SubClients } from "./sub-clients";
import { LogDaysCard } from "./sub-log";
import { SubResponses } from "./sub-rules";
import { SubTemplates } from "./sub-templates";

type Tab = SettingsSearch["tab"];

const ICONS = { general: Globe, subscription: Link2, page: LayoutTemplate, routing: Route, security: ShieldCheck, support: LifeBuoy, system: Cpu, import: ArrowDownToLine } as const;

const PART_LABELS: Record<string, Key> = {
  main: "settings.subParts.main",
  clients: "settings.subParts.clients",
  responses: "settings.subParts.responses",
  templates: "settings.subParts.templates",
  look: "settings.page.tab.look",
  brand: "settings.page.tab.brand",
  blocks: "settings.page.tab.blocks",
  apps: "settings.page.tab.apps",
  docs: "settings.page.tab.docs",
  css: "settings.page.tab.css",
  mode: "settings.routingParts.mode",
  lists: "settings.routingParts.lists",
  rules: "settings.routingParts.rules",
  dns: "settings.routingParts.dns",
  advanced: "settings.routingParts.advanced",
};

/**
 * Settings in sections listed beside the content (a select on phones); the section and its
 * part are in the URL, so a link opens them. Forms keep one readable width; the editors with
 * a live preview take the whole column.
 */
export function SettingsPage() {
  const settings = useSettings();
  // A support operator has no account of their own here: the credentials, the sessions and the keys are closed to them.
  const operator = !!useSupportMode();
  const { tab, part } = useSearch({ from: "/_app/settings" });
  const navigate = useNavigate({ from: "/settings" });
  const go = (next: Tab, nextPart?: string) => void navigate({ search: { tab: next, part: nextPart }, replace: true });
  const routingPart: RoutingPart = ROUTING_PARTS.includes(part as RoutingPart) ? (part as RoutingPart) : "mode";
  const pagePart: SubPagePart = (part as SubPagePart | undefined) ?? "look";
  // The subscription opens on its settings; the apps, how it answers requests and its
  // templates are parts of it.
  const subPart = (SETTINGS_PARTS.subscription as readonly string[]).includes(part ?? "") ? (part as (typeof SETTINGS_PARTS.subscription)[number]) : "main";
  const shownPart = tab === "page" ? pagePart : tab === "routing" ? routingPart : tab === "subscription" ? subPart : undefined;
  const wide = tab === "page" || tab === "routing" || (tab === "subscription" && (subPart === "responses" || subPart === "templates"));
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
                  <AppCard s={s} />
                </>
              ) : tab === "subscription" ? (
                subPart === "clients" ? (
                  <>
                    <SubClients />
                    <XrayJSONCard s={s} />
                  </>
                ) : subPart === "responses" ? (
                  <SubResponses />
                ) : subPart === "templates" ? (
                  <SubTemplates />
                ) : (
                  <>
                    <DevicesCard s={s} />
                    <AppsCard s={s} />
                    <HappCard s={s} />
                    <SubscriptionCard s={s} />
                    <InactiveCard s={s} />
                    <LogDaysCard s={s} />
                    <SubDomainCard s={s} />
                    <SubPortCard s={s} />
                  </>
                )
              ) : tab === "routing" ? (
                <RoutingSection s={s} part={routingPart} onPart={(p) => go("routing", p)} />
              ) : tab === "security" ? (
                operator ? (
                  <>
                    <ClosedForSupport />
                    <CertificateCard s={s} />
                  </>
                ) : (
                  <>
                    <PasswordCard />
                    <TwoFactorCard />
                    <PasskeysCard s={s} />
                    <SessionsCard />
                    <AuditCard />
                    <AccessCard s={s} />
                    <SiteCard s={s} />
                    <CertificateCard s={s} />
                    <ApiCard />
                    <WebhooksCard />
                  </>
                )
              ) : tab === "support" ? (
                // Who may let a helper in is the admin's own business: an operator sees it closed.
                operator ? <ClosedForSupport /> : <SupportCard />
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
