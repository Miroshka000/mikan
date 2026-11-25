import { Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useInbounds, useNodes } from "../../../../api/hooks";
import { Select } from "../../../../components/select";
import { SwitchRow } from "../../../../components/switch";
import { Button, Field, Skeleton } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { nodeLabel } from "../../../../lib/node-label";
import { byNode } from "../../servers-tree";
import { blankAddress, type GroupAddress } from "./model";

// The most addresses one group has: the API's bound.
const MAX = 200;

/**
 * Where the group's clients reach a protocol in place of its default address: an address, a
 * port, a TLS name, an HTTP Host and a name of the group's own, each left empty for what the
 * default address gives. The protocol's other addresses for clients stay as they are: each says
 * itself which groups it is for.
 */
export function AddressesFields({ value, onChange, errors }: { value: GroupAddress[]; onChange: (v: GroupAddress[]) => void; errors: Record<string, string> }) {
  const uid = useId();
  const inbounds = useInbounds();
  const nodes = useNodes();
  const [pick, setPick] = useState("");
  if (inbounds.isPending) return <Skeleton style={{ height: 72 }} />;
  const all = inbounds.data ?? [];
  const taken = new Set(value.map((a) => a.inbound_id));
  const free = byNode(all.filter((i) => !taken.has(i.id)));
  const nodeOf = (id: number) => nodes.data?.find((n) => n.id === id);
  const nodeName = (id: number) => {
    const n = nodeOf(id);
    return n ? nodeLabel(n) : String(id);
  };
  const set = (i: number, patch: Partial<GroupAddress>) => onChange(value.map((a, j) => (j === i ? { ...a, ...patch } : a)));
  const err = (i: number, field: string) => errors[`appearance.addresses[${i}].${field}`];
  return (
    <div className="flex flex-col gap-3">
      {value.length === 0 ? <p className="text-xs text-[var(--ink-500)]">{t("groups.appearance.addressesNone")}</p> : null}
      {value.map((a, i) => {
        const inbound = all.find((x) => x.id === a.inbound_id);
        const node = inbound ? nodeOf(inbound.node_id) : undefined;
        const at = `${uid}-${a.inbound_id}`;
        return (
          <section key={a.inbound_id} className="panel-soft p-3" aria-label={inbound?.sub_name ?? String(a.inbound_id)}>
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="min-w-0">
                <div className="truncate text-[13px] font-medium">{inbound?.sub_name ?? `#${a.inbound_id}`}</div>
                <div className="truncate text-xs text-[var(--ink-500)]">{node ? nodeLabel(node) : ""}</div>
              </div>
              <Button size="sm" variant="ghost" aria-label={t("groups.appearance.addressRemove")} title={t("groups.appearance.addressRemove")} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <Trash2 size={16} aria-hidden />
              </Button>
            </div>
            {err(i, "inbound_id") ? (
              <p className="mb-2 text-xs text-[var(--berry-600)]" role="alert">
                {err(i, "inbound_id")}
              </p>
            ) : null}
            <div className="grid gap-x-3 sm:grid-cols-[1fr_120px]">
              <Field label={t("groups.appearance.address")} htmlFor={`${at}-address`} error={err(i, "address")}>
                <input id={`${at}-address`} className="input mono" value={a.address} maxLength={253} spellCheck={false} autoCapitalize="off" placeholder={t("inbounds.hosts.nodeAddress")} aria-invalid={!!err(i, "address")} onChange={(e) => set(i, { address: e.target.value })} />
              </Field>
              <Field label={t("groups.appearance.port")} htmlFor={`${at}-port`} error={err(i, "port")}>
                <input
                  id={`${at}-port`}
                  className="input num"
                  inputMode="numeric"
                  value={a.port || ""}
                  maxLength={5}
                  placeholder={inbound?.port ?? ""}
                  aria-invalid={!!err(i, "port")}
                  onChange={(e) => set(i, { port: Math.min(65535, Number(e.target.value.replace(/\D/g, "")) || 0) })}
                />
              </Field>
            </div>
            <div className="grid gap-x-3 sm:grid-cols-2">
              <Field label="SNI" htmlFor={`${at}-sni`} error={err(i, "sni")}>
                <input id={`${at}-sni`} className="input mono" value={a.sni} maxLength={253} spellCheck={false} autoCapitalize="off" aria-invalid={!!err(i, "sni")} onChange={(e) => set(i, { sni: e.target.value })} />
              </Field>
              <Field label="Host" htmlFor={`${at}-host`} error={err(i, "host")}>
                <input id={`${at}-host`} className="input mono" value={a.host} maxLength={253} spellCheck={false} autoCapitalize="off" aria-invalid={!!err(i, "host")} onChange={(e) => set(i, { host: e.target.value })} />
              </Field>
            </div>
            <Field label={t("groups.appearance.addressName")} htmlFor={`${at}-name`} hint={t("groups.appearance.addressNameHint")} error={err(i, "name")}>
              <input id={`${at}-name`} className="input" value={a.name} maxLength={48} placeholder={inbound?.sub_name ?? ""} aria-invalid={!!err(i, "name")} onChange={(e) => set(i, { name: e.target.value })} />
            </Field>
            <SwitchRow label={t("groups.appearance.cdn")} sub={t("groups.appearance.cdnSub")} checked={a.cdn} onChange={(cdn) => set(i, { cdn })} />
          </section>
        );
      })}
      {errors["appearance.addresses"] ? (
        <p className="text-xs text-[var(--berry-600)]" role="alert">
          {errors["appearance.addresses"]}
        </p>
      ) : null}
      {free.length > 0 && value.length < MAX ? (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={t("groups.appearance.addressFor")} htmlFor={`${uid}-pick`}>
            <Select id={`${uid}-pick`} className="input min-w-[220px]" value={pick} onChange={(e) => setPick(e.target.value)}>
              <option value="">{t("groups.appearance.addressPick")}</option>
              {free.map(([node, list]) => (
                <optgroup key={node} label={nodeName(node)}>
                  {list.map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.sub_name}
                    </option>
                  ))}
                </optgroup>
              ))}
            </Select>
          </Field>
          <Button
            className="mb-4"
            disabled={!pick}
            onClick={() => {
              onChange([...value, blankAddress(Number(pick))]);
              setPick("");
            }}
          >
            <Plus size={16} aria-hidden /> {t("groups.appearance.addressAdd")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
