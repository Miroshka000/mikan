import { useId, useState, type FormEvent } from "react";
import type { Schemas } from "../../../api/client";
import { useInbounds } from "../../../api/hooks";
import { Button, Field, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { byNode, TariffServersTree, type ServersDraft } from "../servers-tree";
import { ChipFields, type ChipDraft } from "../user-folders";
import { groupServersBody } from "./model";

export type GroupDraft = ChipDraft & { servers: ServersDraft };

/** What the form sends: the chip's fields and the servers. */
export type GroupBody = Schemas["CreateGroupInputBody"];

/**
 * A group's name, look and access: its servers are the client's tariff's, or the chosen ones
 * in place of them. errors are the API's refusals by field (name, servers).
 */
export function GroupForm({
  initial,
  submit,
  busy,
  errors,
  onCancel,
}: {
  initial: GroupDraft;
  submit: (b: GroupBody) => void;
  busy: boolean;
  errors: Record<string, string | undefined>;
  onCancel?: () => void;
}) {
  const uid = useId();
  const inbounds = useInbounds();
  const [draft, setDraft] = useState(initial);
  const servers = draft.servers;
  const empty = !servers.all && servers.nodes.size === 0 && servers.inbounds.size === 0;
  const ok = draft.name.trim() !== "" && !empty;
  const send = (e: FormEvent) => {
    e.preventDefault();
    if (ok && !busy) submit({ name: draft.name.trim(), emoji: draft.emoji.trim(), color: draft.color, servers: groupServersBody(servers) });
  };
  const serversError = errors.servers ?? errors["servers.nodes"] ?? errors["servers.inbounds"];
  return (
    <form onSubmit={send} noValidate className="flex flex-col gap-4">
      <ChipFields uid={uid} value={draft} onChange={(c) => setDraft({ ...draft, ...c })} error={errors.name} namePlaceholder={t("groups.namePlaceholder")} />
      {/* A choice of nothing cannot be saved: the hint says what is missing, the button waits. */}
      <Field label={t("groups.servers")} hint={empty ? t("groups.serversPick") : t("groups.serversHint")} error={serversError}>
        <Segmented
          label={t("groups.servers")}
          value={servers.all ? "tariff" : "chosen"}
          onChange={(v) => setDraft({ ...draft, servers: { ...servers, all: v === "tariff" } })}
          options={[
            { value: "tariff", label: t("groups.serversTariff") },
            { value: "chosen", label: t("groups.serversChosen") },
          ]}
        />
        {servers.all ? null : inbounds.isPending ? (
          <Skeleton className="mt-2" style={{ height: 96 }} />
        ) : (inbounds.data ?? []).length === 0 ? (
          <p className="mt-2 text-xs text-[var(--ink-500)]">{t("pools.noInbounds")}</p>
        ) : (
          <div className="mt-2">
            <TariffServersTree nodes={byNode(inbounds.data)} value={servers} onChange={(s) => setDraft({ ...draft, servers: s })} />
          </div>
        )}
      </Field>
      <div className="flex justify-end gap-2">
        {onCancel ? (
          <Button variant="ghost" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        ) : null}
        <Button variant="primary" type="submit" loading={busy} disabled={!ok}>
          {onCancel ? t("common.save") : t("groups.add")}
        </Button>
      </div>
    </form>
  );
}
