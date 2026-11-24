import type { ReactNode } from "react";
import type { User } from "../../../../api/client";
import { useGroups, useTariffs } from "../../../../api/hooks";
import { Pill } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { ruleLabel } from "../../../../lib/sub-rules";
import { GroupBadge } from "../../groups/badge";
import { choiceName } from "../../settings/sub-templates/model";
import { madeByTemplate, OUTCOME_TONE, serversFrom, type Preview } from "./model";

/**
 * The answer in short: what went out and with what code, the app the User-Agent names, what
 * decided, the format and the template, why a stub went out, what the device would get and
 * where the client's servers come from.
 */
export function PreviewSummary({ p, u }: { p: Preview; u: User }) {
  return (
    <dl className="panel-soft grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-2 p-4 text-[13px]">
      <Row label={t("userDrawer.preview.answer")}>
        <Pill tone={OUTCOME_TONE[p.outcome]}>{p.outcome === "error" ? t("userDrawer.preview.outcomeError") : t(`userDrawer.subLog.outcome.${p.outcome}`)}</Pill>
        <span className="text-[var(--ink-500)]">{p.status ? t("userDrawer.preview.status", { code: p.status }) : t("userDrawer.preview.closed")}</span>
      </Row>
      <Row label={t("settings.subRules.test.app")}>{p.app_name || t("settings.subRules.test.unknownApp")}</Row>
      <Row label={t("settings.subRules.test.decided")}>{p.position > 0 ? `${ruleLabel(p.rule)} · ${t("settings.subRules.test.position", { n: p.position })}` : ruleLabel(p.rule)}</Row>
      {p.format ? <Row label={t("userDrawer.preview.formatLabel")}>{t(`userDrawer.preview.formats.${p.format}`)}</Row> : null}
      {madeByTemplate(p) ? (
        <Row label={t("settings.subRules.test.template")}>
          <Template p={p} />
        </Row>
      ) : null}
      {p.stub ? <Row label={t("userDrawer.preview.stubLabel")}>{t(`userDrawer.preview.stub.${p.stub}`)}</Row> : null}
      {p.device ? <Row label={t("userDrawer.preview.deviceLabel")}>{t(`userDrawer.preview.device.${p.device}`)}</Row> : null}
      <Row label={t("userDrawer.preview.servers")}>
        <Servers p={p} u={u} />
      </Row>
    </dl>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-[var(--ink-500)]">{label}</dt>
      <dd className="flex min-w-0 flex-wrap items-center gap-2 break-words">{children}</dd>
    </>
  );
}

// The own template the profile is made of; one that did not render for this client is said so.
function Template({ p }: { p: Preview }) {
  if (!p.template) return <>{t("settings.subRules.test.builtinProfile")}</>;
  if (p.template_failed) return <span className="text-[var(--honey-600)]">{t("userDrawer.preview.templateFailed", { name: p.template.name || `#${p.template.id}` })}</span>;
  return <>{choiceName({ name: p.template.name || `#${p.template.id}`, kind: p.template.kind })}</>;
}

// Where the client's servers come from: the group by its badge, the tariff by its name.
function Servers({ p, u }: { p: Preview; u: User }) {
  const groups = useGroups();
  const tariffs = useTariffs();
  const from = serversFrom(p, u.tariff_id);
  if (from === "group") {
    const group = groups.data?.find((g) => g.id === p.group_id);
    return (
      <>
        {t("userDrawer.preview.serversGroup")}
        {group ? <GroupBadge group={group} viaTariff={u.group_id == null} /> : null}
      </>
    );
  }
  if (from === "tariff") {
    const tariff = tariffs.data?.find((x) => x.id === u.tariff_id);
    return (
      <>
        {t("userDrawer.preview.serversTariff")}
        {tariff ? <span className="font-medium">{tariff.name}</span> : null}
      </>
    );
  }
  return <>{t(from === "own" ? "userDrawer.preview.serversOwn" : "userDrawer.preview.serversAll")}</>;
}
