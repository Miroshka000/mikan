import { ChevronRight } from "lucide-react";
import { useId } from "react";
import type { Tariff } from "../../../api/client";
import { useFolders, useGroups } from "../../../api/hooks";
import { Select } from "../../../components/select";
import { SwitchRow } from "../../../components/switch";
import { MAX_TAGS } from "../../../components/tags";
import { Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes, days, emojiName, GiB } from "../../../lib/format";
import { tariffTerm } from "../tariffs";
import { LimitPicker } from "../user/limit-picker";
import type { Draft, Limit } from "./model";

type SetDraft = (f: (d: Draft) => Draft) => void;
type Errors = Partial<Record<string, string>>;
type LimitKey = "traffic" | "devices" | "speed" | "term";

/** A limit's own value in words, or none: what the folded section says it holds. */
function limitWord(v: Limit, word: (n: number) => string): string | null {
  if (v === undefined) return null;
  return v === null ? "∞" : word(v);
}

const devicesWord = (n: number) => t("userDrawer.devicesShort", { n });
const speedWord = (n: number) => t("tariffs.speedShort", { n });

/**
 * What a new client gets over its tariff: the traffic, the devices, the speed and the term,
 * each as in the tariff unless set here, and whether the term waits for the first connection.
 * Folded away while everything is as in the tariff: the usual case needs none of it.
 */
export function OverTariff({ draft, setDraft, tariff }: { draft: Draft; setDraft: SetDraft; tariff?: Tariff }) {
  const set = (k: LimitKey, v: Limit) => setDraft((d) => ({ ...d, [k]: v }));
  // The first choice of each limit says what the tariff gives: "as in the tariff · 150 GB".
  const inherit = (k: LimitKey, said: (tr: Tariff) => string) => ({ label: t("userCreate.asTariff", { v: tariff ? said(tariff) : "…" }), onPick: () => set(k, undefined) });
  const orNone = (v: number | null, word: (n: number) => string) => (v == null ? "∞" : word(v));
  const pick = (k: LimitKey, scale = 1) => (n: number | null, done: () => void) => {
    set(k, n === null ? null : n * scale);
    done();
  };
  const own = [limitWord(draft.traffic, bytes), limitWord(draft.devices, devicesWord), limitWord(draft.speed, speedWord), limitWord(draft.term, days)].filter(
    (x): x is string => x !== null,
  );
  // The wait means nothing to a term that never ends.
  const hasTerm = draft.term !== null && (draft.term !== undefined || (tariff?.duration_days ?? 0) > 0);
  return (
    <details className="disclosure" open={own.length > 0 || draft.wait !== undefined || undefined}>
      <summary>
        <ChevronRight size={16} aria-hidden />
        <span className="disclosure-title">{t("userCreate.overTariff")}</span>
        <span className="disclosure-sub truncate">{own.length ? own.join(" · ") : t("userCreate.overTariffNone")}</span>
      </summary>
      <div className="disclosure-body flex flex-col gap-4">
        <LimitPicker
          label={t("userCreate.traffic")}
          value={draft.traffic == null ? draft.traffic : draft.traffic / GiB}
          presets={[10, 50, 100, 500]}
          min={1}
          max={100_000}
          format={(n) => t("userCreate.gb", { n })}
          fieldUnit={t("userCreate.gbUnit")}
          inherit={inherit("traffic", (tr) => orNone(tr.traffic_limit, bytes))}
          onChange={pick("traffic", GiB)}
        />
        <LimitPicker
          label={t("userCreate.devices")}
          value={draft.devices}
          presets={[1, 2, 3, 5]}
          min={1}
          max={100}
          inherit={inherit("devices", (tr) => orNone(tr.device_limit, devicesWord))}
          onChange={pick("devices")}
        />
        <LimitPicker
          label={t("userCreate.speed")}
          value={draft.speed}
          presets={[10, 50, 100]}
          min={1}
          max={100_000}
          unit={t("userCreate.mbps")}
          inherit={inherit("speed", (tr) => orNone(tr.speed_limit, speedWord))}
          onChange={pick("speed")}
        />
        <LimitPicker
          label={t("userCreate.term")}
          value={draft.term}
          presets={[7, 30, 90, 365]}
          min={1}
          max={3650}
          format={days}
          fieldUnit={t("userCreate.daysUnit")}
          inherit={inherit("term", tariffTerm)}
          onChange={pick("term")}
        />
        {hasTerm ? (
          <SwitchRow label={t("userCreate.wait")} sub={t("userCreate.waitSub")} checked={draft.wait ?? tariff?.starts_on_connect ?? false} onChange={(v) => setDraft((d) => ({ ...d, wait: v }))} />
        ) : null}
      </div>
    </details>
  );
}

/** A folder or a group of the new client: none of them, or one of the list by its icon and name. */
function PlaceSelect({
  id,
  label,
  hint,
  error,
  none,
  list,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  none: string;
  list: { id: number; emoji: string; name: string }[];
  value: number | null;
  onChange: (id: number | null) => void;
}) {
  // A folder or a group deleted since the template was saved is none.
  const chosen = list.some((x) => x.id === value) ? String(value) : "";
  return (
    <Field label={label} htmlFor={id} hint={hint} error={error}>
      <Select id={id} className="input" value={chosen} onChange={(e) => onChange(e.target.value ? Number(e.target.value) : null)}>
        <option value="">{none}</option>
        {list.map((x) => (
          <option key={x.id} value={x.id}>
            {emojiName(x)}
          </option>
        ))}
      </Select>
    </Field>
  );
}

/** Where a new client is put: the folder, the group, the labels and the note. */
export function Placement({ draft, setDraft, errors }: { draft: Draft; setDraft: SetDraft; errors: Errors }) {
  const uid = useId();
  const folders = useFolders().data ?? [];
  const groups = useGroups().data ?? [];
  const folder = folders.find((f) => f.id === draft.folderId);
  const group = groups.find((g) => g.id === draft.groupId);
  const said = [folder?.name, group?.name, draft.tags.trim()].filter((x): x is string => !!x);
  // Open while it holds something, or when the server refused what it holds.
  const open = said.length > 0 || !!draft.note.trim() || !!(errors.folder_id || errors.group_id || errors.tags);
  return (
    <details className="disclosure" open={open || undefined}>
      <summary>
        <ChevronRight size={16} aria-hidden />
        <span className="disclosure-title">{t("userCreate.placement")}</span>
        <span className="disclosure-sub truncate">{said.length ? said.join(" · ") : t("userCreate.placementNone")}</span>
      </summary>
      <div className="disclosure-body">
        <div className="grid gap-x-3 sm:grid-cols-2">
          <PlaceSelect
            id={`${uid}-folder`}
            label={t("userCreate.folder")}
            error={errors.folder_id}
            none={t("userCreate.noFolder")}
            list={folders}
            value={draft.folderId}
            onChange={(folderId) => setDraft((d) => ({ ...d, folderId }))}
          />
          <PlaceSelect
            id={`${uid}-group`}
            label={t("userCreate.group")}
            hint={t("userCreate.groupHint")}
            error={errors.group_id}
            none={t("userCreate.groupOfTariff")}
            list={groups}
            value={draft.groupId}
            onChange={(groupId) => setDraft((d) => ({ ...d, groupId }))}
          />
        </div>
        <Field label={t("tags.label")} htmlFor={`${uid}-tags`} hint={t("userCreate.tagsHint", { n: MAX_TAGS })} error={errors.tags}>
          <input id={`${uid}-tags`} className="input" value={draft.tags} onChange={(e) => setDraft((d) => ({ ...d, tags: e.target.value }))} placeholder={t("userCreate.tagsPlaceholder")} autoComplete="off" aria-invalid={!!errors.tags} />
        </Field>
        <Field label={t("userCreate.note")} htmlFor={`${uid}-note`} error={errors.note}>
          <textarea id={`${uid}-note`} className="input min-h-[72px]" value={draft.note} maxLength={2000} onChange={(e) => setDraft((d) => ({ ...d, note: e.target.value }))} />
        </Field>
      </div>
    </details>
  );
}
