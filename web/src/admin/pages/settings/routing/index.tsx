import { Link, useBlocker } from "@tanstack/react-router";
import { Eye } from "lucide-react";
import { useState } from "react";
import { ApiError, errorText, type Schemas } from "../../../../api/client";
import { useExitDns, useSubTemplates } from "../../../../api/hooks";
import { Card, SaveBar } from "../../../../components/layout";
import { Confirm, Drawer } from "../../../../components/overlay";
import { Button, ErrorState, Pill, Skeleton } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { useDraft } from "../../../../lib/draft";
import { useSaveSettings } from "../shared";
import { AdvancedPart } from "./advanced";
import { DeviceDnsCard, useDeviceDns } from "./dns";
import { ExitDnsFields, exitDnsDraftOf, useSaveExitDns, type ExitDnsDraft } from "./exit-dns";
import { ListsPart } from "./lists";
import { ModePart } from "./mode";
import { serversRefused, tidy, type Routes, type RoutingPart } from "./model";
import { PreviewCard } from "./preview";
import { RulesPart } from "./rules";

export { ROUTING_PARTS, type RoutingPart } from "./model";

/** The part a refusal of the server names, so a save from another part shows where it went wrong. */
function partOf(e: ApiError): RoutingPart | null {
  if (e.fields.sub_rules) return "rules";
  if (serversRefused(e.codes.sub_routes)) return "advanced";
  if (e.fields.sub_routes) return "lists";
  return null;
}

/**
 * The routing of the subscriptions, a part at a time: the mode and the services, the own
 * lists and the apps past the tunnel, the own rules, DNS on the device and at the exit, the
 * server groups and fine tuning. One draft for all of it, saved from one bar; the profile a
 * client gets opens beside it on demand. A default Clash template takes its place for the
 * Clash apps, and the section says so.
 */
export function RoutingSection({ s, part, onPart }: { s: Schemas["SettingsView"]; part: RoutingPart; onPart: (p: RoutingPart) => void }) {
  const save = useSaveSettings();
  const exitQ = useExitDns();
  const saveExit = useSaveExitDns();
  const { draft, setDraft, dirty: routingDirty, reset } = useDraft({ mode: s.sub_routing, routes: s.sub_routes, rules: s.sub_rules });
  const exit = useDraft<ExitDnsDraft | null>(exitQ.data ? exitDnsDraftOf(exitQ.data) : null);
  const routes = draft.routes;
  const setRoutes = (f: (r: Routes) => Routes) => setDraft((d) => ({ ...d, routes: tidy(f(d.routes)) }));
  // The DNS fields keep their own text, so they are put back with the rest.
  const dns = useDeviceDns(routes.dns, setRoutes);
  const [preview, setPreview] = useState(false);
  const dirty = routingDirty || exit.dirty;
  const discard = () => {
    reset();
    exit.reset();
    dns.reset(s.sub_routes.dns);
  };
  const submit = () => {
    if (routingDirty) {
      save.mutate(
        { sub_routing: draft.mode, sub_routes: draft.routes, sub_rules: draft.rules },
        {
          onError: (e) => {
            const p = e instanceof ApiError ? partOf(e) : null;
            if (p && p !== part) onPart(p);
          },
        },
      );
    }
    if (exit.dirty && exit.draft) {
      saveExit.mutate(exit.draft, { onError: () => part !== "dns" && onPart("dns") });
    }
  };
  const apiErr = save.error instanceof ApiError ? save.error : null;
  const errors = apiErr?.fields ?? {};
  const templates = useSubTemplates();
  const live = !!templates.data?.templates.some((x) => x.core === "mihomo" && x.default);
  // The settings show one tab at a time: leaving the routing (or the page) drops the draft,
  // so ask first. Its parts are one draft.
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => dirty && (current.pathname !== next.pathname || (next.search as { tab?: string }).tab !== "routing"),
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">{live ? <Pill tone="warn">{t("settings.routesTemplateOn")}</Pill> : null}</div>
        <Button size="sm" onClick={() => setPreview(true)} aria-haspopup="dialog">
          <Eye size={16} aria-hidden /> {t("settings.routesShowProfile")}
        </Button>
      </div>
      {live ? (
        <div className="banner info flex-wrap">
          <span className="min-w-0 flex-1">{t("settings.routesTemplateLive")}</span>
          <Link to="/settings" search={{ tab: "subscription", part: "templates" }} className="btn btn-glass btn-sm">
            {t("settings.routesToTemplates")}
          </Link>
        </div>
      ) : null}

      {part === "mode" ? <ModePart mode={draft.mode} setMode={(mode) => setDraft((d) => ({ ...d, mode }))} routes={routes} setRoutes={setRoutes} /> : null}
      {part === "lists" ? <ListsPart routes={routes} setRoutes={setRoutes} error={serversRefused(apiErr?.codes.sub_routes) ? undefined : errors.sub_routes} /> : null}
      {part === "rules" ? (
        <RulesPart
          value={draft.rules}
          onChange={(f) => setDraft((d) => ({ ...d, rules: f(d.rules) }))}
          error={errors.sub_rules}
          badLine={errors.sub_rules && typeof apiErr?.values.sub_rules === "number" ? apiErr.values.sub_rules : 0}
          targets={s.rule_targets}
          sub={t("settings.routingPartsSub.rules")}
        />
      ) : null}
      {part === "dns" ? (
        <>
          <DeviceDnsCard dns={dns} />
          <Card title={t("settings.exitDns.title")} sub={t("settings.exitDns.lead")} i={2}>
            {exitQ.isError ? <ErrorState text={errorText(exitQ.error)} onRetry={() => void exitQ.refetch()} /> : null}
            {exitQ.data && exit.draft ? (
              <ExitDnsFields v={exitQ.data} draft={exit.draft} setDraft={(f) => exit.setDraft((d) => (d ? f(d) : d))} error={saveExit.error} />
            ) : exitQ.isPending ? (
              <Skeleton className="h-32 w-full rounded-2xl" />
            ) : null}
          </Card>
        </>
      ) : null}
      {part === "advanced" ? (
        <AdvancedPart
          routes={routes}
          setRoutes={setRoutes}
          error={errors.sub_routes && serversRefused(apiErr?.codes.sub_routes) ? { text: errors.sub_routes, ofCore: apiErr?.codes.sub_routes !== "routes_servers" } : undefined}
        />
      ) : null}
      <p className="px-1 text-xs text-[var(--ink-500)]">{t("settings.routesNote")}</p>

      <SaveBar dirty={dirty} saving={save.isPending || saveExit.isPending} onSave={submit} onReset={discard} />

      <Drawer open={preview} onOpenChange={setPreview} title={t("settings.routesPreview")}>
        <div className="pt-4">{preview ? <PreviewCard mode={draft.mode} routes={draft.routes} rules={draft.rules} /> : null}</div>
      </Drawer>
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("settings.page.leaveTitle")}
        text={t("settings.routesLeaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
    </div>
  );
}
