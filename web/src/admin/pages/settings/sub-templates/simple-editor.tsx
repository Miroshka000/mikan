import { useMutation } from "@tanstack/react-query";
import { FileCode2, ListTree, Route, SlidersHorizontal } from "lucide-react";
import { useMemo, useState } from "react";
import { api, ApiError, errorText, unwrap } from "../../../../api/client";
import { useSettings } from "../../../../api/hooks";
import { Card, SaveBar, WithPreview } from "../../../../components/layout";
import { Confirm } from "../../../../components/overlay";
import { QueryBoundary } from "../../../../components/query";
import { Tabs } from "../../../../components/tabs";
import { useToast } from "../../../../components/toast";
import { Button, Segmented, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { same } from "../../../../lib/draft";
import { fieldErrors } from "../../../../lib/fields";
import { AdvancedPart } from "../routing/advanced";
import { DeviceDnsCard, useDeviceDns } from "../routing/dns";
import { ListsPart } from "../routing/lists";
import { ModePart } from "../routing/mode";
import { serversRefused, tidy, type SetRoutes } from "../routing/model";
import { RulesPart } from "../routing/rules";
import { BackButton, NameFields, useLeaveGuard, useSaveTemplate } from "./editor-parts";
import { LivePreview } from "./live-preview";
import { CORES, coreName, firstOfCore, type Core, type Saved, type TemplateRouting, type View } from "./model";
import type { TextStart } from "./text-editor";

/** The parts of a simple template's routing, a tab each: the mode and the services, the lists
 * and the apps past the tunnel, the own rules, and DNS with the finer settings. */
const PARTS = [
  { id: "mode", label: "settings.subTemplates.simple.parts.mode", icon: Route },
  { id: "lists", label: "settings.subTemplates.simple.parts.lists", icon: ListTree },
  { id: "rules", label: "settings.subTemplates.simple.parts.rules", icon: FileCode2 },
  { id: "more", label: "settings.subTemplates.simple.parts.more", icon: SlidersHorizontal },
] as const;
type Part = (typeof PARTS)[number]["id"];

/** The part a refusal of the routing is about: the rules', or the routes' by its code. */
function partOf(e: unknown): Part | null {
  if (!(e instanceof ApiError)) return null;
  if (e.fields["routing.rules"]) return "rules";
  const code = e.codes["routing.routes"];
  if (!code) return null;
  if (serversRefused(code) || code.startsWith("routes_dns")) return "more";
  return code === "routes_service" ? "mode" : "lists";
}

/**
 * A simple template: a routing of its own in place of a text, the built-in profile of every
 * core made by it. A new one starts as a copy of the routing settings, which also give the
 * targets of its rules.
 */
export function SimpleEditor({ v, saved, onClose, onSaved, onAsText }: { v: View; saved?: Saved; onClose: () => void; onSaved: (t: Saved) => void; onAsText: (start: TextStart) => void }) {
  const settings = useSettings();
  return (
    <QueryBoundary query={settings} pending={<Skeleton style={{ height: 480, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(s) => (
        <SimpleForm
          v={v}
          saved={saved}
          start={saved?.routing ?? { mode: s.sub_routing, routes: s.sub_routes, rules: s.sub_rules }}
          targets={s.rule_targets}
          onClose={onClose}
          onSaved={onSaved}
          onAsText={onAsText}
        />
      )}
    </QueryBoundary>
  );
}

function SimpleForm({
  v,
  saved,
  start,
  targets,
  onClose,
  onSaved,
  onAsText,
}: {
  v: View;
  saved?: Saved;
  start: TemplateRouting;
  targets: string[];
  onClose: () => void;
  onSaved: (t: Saved) => void;
  onAsText: (start: TextStart) => void;
}) {
  useLocale();
  const toast = useToast();
  const startName = saved?.name ?? t("settings.subTemplates.simple.name");
  const startDef = saved ? saved.default : firstOfCore(v, "mihomo");
  const [name, setName] = useState(startName);
  const [def, setDef] = useState(startDef);
  const [routing, setRouting] = useState(start);
  const [part, setPart] = useState<Part>("mode");
  const [core, setCore] = useState<Core>("mihomo");
  const [asText, setAsText] = useState(false);
  const hidden = !!saved?.hidden;
  const setRoutes: SetRoutes = (f) => setRouting((r) => ({ ...r, routes: tidy(f(r.routes)) }));
  const dns = useDeviceDns(routing.routes.dns, setRoutes);
  const changed = name !== startName || def !== startDef || (!hidden && !same(routing, start));
  const save = useSaveTemplate(saved, "mihomo", onSaved, (e) => {
    const p = partOf(e);
    if (p) setPart(p);
  });
  const guard = useLeaveGuard(changed, save.isPending, onClose);
  // The YAML of the routing as it stands, saved or not: "Open as text" puts it in the text editor.
  const toText = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/sub-templates/preview", { body: { core: "mihomo", routing, starter: true } })).then((r) => r.profile),
    onSuccess: (text) => onAsText({ text, name, def, fromSimple: true }),
    onError: (e) => toast.error(errorText(e)),
  });
  const previewOf = useMemo(() => ({ core, routing }), [core, routing]);
  const errors = fieldErrors(save.error);
  const badLine = save.error instanceof ApiError ? save.error.values["routing.rules"] : undefined;
  const discard = () => {
    setName(startName);
    setDef(startDef);
    setRouting(start);
    dns.reset(start.routes.dns);
  };
  const coreSwitch = <Segmented label={t("settings.subTemplates.simple.previewCore")} value={core} onChange={setCore} options={CORES.map((c) => ({ value: c, label: coreName(c) }))} />;
  return (
    <div className="flex flex-col gap-4">
      <BackButton onClick={guard.back} />
      <WithPreview title={t("settings.subTemplates.preview")} preview={(bare) => (hidden ? null : <LivePreview of={previewOf} aside={coreSwitch} bare={bare} />)}>
        <Card title={saved ? saved.name : t("settings.subTemplates.simple.newTitle")} sub={t("settings.subTemplates.simple.lead")}>
          <NameFields name={name} setName={setName} def={def} setDef={setDef} error={errors.name} defaultSub={t("settings.subTemplates.simple.defaultSub")} />
          {hidden ? (
            <div className="banner info mt-3">{t("settings.subTemplates.simple.hidden")}</div>
          ) : (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <Button variant="ghost" size="sm" loading={toText.isPending} onClick={() => setAsText(true)}>
                {t("settings.subTemplates.asText.open")}
              </Button>
              <span className="text-xs text-[var(--ink-500)]">{t("settings.subTemplates.asText.hint")}</span>
            </div>
          )}
        </Card>
        {hidden ? null : (
          <Tabs id="simple-template" label={t("settings.subTemplates.simple.partsLabel")} tabs={PARTS.map((p) => ({ id: p.id, label: t(p.label), icon: p.icon }))} value={part} onChange={setPart}>
            {errors["routing.routes"] ? (
              <div className="banner err" role="alert">
                {errors["routing.routes"]}
              </div>
            ) : null}
            {part === "mode" ? <ModePart mode={routing.mode} setMode={(mode) => setRouting((r) => ({ ...r, mode }))} routes={routing.routes} setRoutes={setRoutes} /> : null}
            {part === "lists" ? <ListsPart routes={routing.routes} setRoutes={setRoutes} /> : null}
            {part === "rules" ? (
              <RulesPart
                value={routing.rules}
                onChange={(f) => setRouting((r) => ({ ...r, rules: f(r.rules) }))}
                error={errors["routing.rules"]}
                badLine={typeof badLine === "number" ? badLine : 0}
                targets={targets}
                sub={t("settings.subTemplates.simple.rulesSub")}
              />
            ) : null}
            {part === "more" ? (
              <>
                <DeviceDnsCard dns={dns} />
                <AdvancedPart routes={routing.routes} setRoutes={setRoutes} />
              </>
            ) : null}
          </Tabs>
        )}
      </WithPreview>
      <SaveBar
        dirty={changed || !saved}
        saving={save.isPending}
        onSave={() => save.mutate({ name, def, content: hidden ? {} : { routing } })}
        onReset={discard}
        label={saved ? undefined : t("settings.subTemplates.simple.unsavedNew")}
      />
      <Confirm
        open={asText}
        onOpenChange={setAsText}
        title={t("settings.subTemplates.asText.title")}
        text={t("settings.subTemplates.asText.text")}
        confirm={t("settings.subTemplates.asText.confirm")}
        loading={toText.isPending}
        onConfirm={() => {
          setAsText(false);
          toText.mutate();
        }}
      />
      {guard.dialog}
    </div>
  );
}
