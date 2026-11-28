import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ChangeEvent, FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useFilters } from "../../api/hooks";
import { CardHead, FormActions } from "../../components/layout";
import { QueryBoundary } from "../../components/query";
import { SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Field, PageHeader, Segmented, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { splitList } from "../../lib/list";
import { BackToAddons } from "./addon-row";
import { FilterRules } from "./filter-rules";
import { TrafficDetailCard } from "./traffic-detail";

type View = Schemas["FiltersView"];

const text = (l: string[]) => l.join("\n");

function useSaveFilters() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (body: Schemas["PatchFiltersInputBody"]) => unwrap(api.PATCH("/api/v1/filters", { body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.filters, v);
      toast.ok(t("filters.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

// The first wrong entry of a list: the API names it "egress.ports[2]".
function listError(errors: Record<string, string>, list: string): string | undefined {
  const k = Object.keys(errors).find((k) => k === list || k.startsWith(`${list}[`));
  return k ? errors[k] : undefined;
}

/** The ingress and egress filters: built into the nodes, switched on here. */
export function FiltersPage() {
  const q = useFilters();
  return (
    <>
      <PageHeader title={t("filters.title")} sub={t("filters.sub")} actions={<BackToAddons />} />
      <QueryBoundary query={q} pending={<Skeleton style={{ height: 320 }} />}>
        {(v) => (
          <div className="flex max-w-4xl flex-col gap-4">
            <EgressCard v={v} />
            <IngressCard v={v} />
            <TrafficDetailCard />
            <p className="text-xs text-[var(--ink-500)]">{t("filters.nodes")}</p>
          </div>
        )}
      </QueryBoundary>
    </>
  );
}

const egressDraft = ({ egress: e }: View) => ({ enabled: e.enabled, mail: e.mail, ports: text(e.ports), networks: text(e.networks), domains: text(e.domains), rules: e.rules ?? [] });
const ingressDraft = ({ ingress: i }: View) => ({ enabled: i.enabled, mode: (i.allow ? "allow" : "deny") as "allow" | "deny", networks: text(i.networks) });

function EgressCard({ v }: { v: View }) {
  const save = useSaveFilters();
  const { draft, setDraft, dirty, reset } = useDraft(egressDraft(v));
  const errors = save.variables?.egress ? fieldErrors(save.error) : {};
  const edit = (k: "ports" | "networks" | "domains") => (ev: ChangeEvent<HTMLTextAreaElement>) => setDraft((d) => ({ ...d, [k]: ev.target.value }));
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    // The panel puts the lists in order: the form takes them as saved.
    save.mutate({ egress: { enabled: draft.enabled, mail: draft.mail, ports: splitList(draft.ports), networks: splitList(draft.networks), domains: splitList(draft.domains), rules: draft.rules } }, { onSuccess: (nv) => setDraft(egressDraft(nv)) });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <CardHead title={t("filters.egress")} sub={t("filters.egressSub")} />
        <SwitchRow label={t("filters.egressOn")} sub={t("filters.always")} checked={draft.enabled} onChange={(enabled) => setDraft((d) => ({ ...d, enabled }))} />
        <SwitchRow label={t("filters.mail")} sub={t("filters.mailHint", { ports: v.mail_ports.join(", ") })} checked={draft.mail} onChange={(mail) => setDraft((d) => ({ ...d, mail }))} />
        <Field label={t("filters.ports")} htmlFor="f-ports" hint={t("filters.portsHint")} error={listError(errors, "egress.ports")}>
          <textarea id="f-ports" className="input mono min-h-[72px]" value={draft.ports} onChange={edit("ports")} spellCheck={false} placeholder={"6881-6889\n1194"} />
        </Field>
        <Field label={t("filters.networks")} htmlFor="f-networks" hint={t("filters.networksHint")} error={listError(errors, "egress.networks")}>
          <textarea id="f-networks" className="input mono min-h-[72px]" value={draft.networks} onChange={edit("networks")} spellCheck={false} placeholder={"203.0.113.0/24\n198.51.100.7"} />
        </Field>
        <Field label={t("filters.domains")} htmlFor="f-domains" hint={t("filters.domainsHint")} error={listError(errors, "egress.domains")}>
          <textarea id="f-domains" className="input mono min-h-[72px]" value={draft.domains} onChange={edit("domains")} spellCheck={false} placeholder="example.com" />
        </Field>
        <FilterRules
          rules={draft.rules}
          globalValues={{ port: splitList(draft.ports), network: splitList(draft.networks), domain: splitList(draft.domains) }}
          error={listError(errors, "egress.rules")}
          onRemove={(index) => setDraft((d) => ({ ...d, rules: d.rules.filter((_, i) => i !== index) }))}
          onSave={(index, { scope, ...rule }) => setDraft((d) => {
            const key = rule.kind === "network" ? "networks" : rule.kind === "port" ? "ports" : "domains";
            const rules = d.rules.filter((_, i) => i !== index);
            const globals = splitList(d[key]).filter((value) => value !== rule.value);
            if (scope === "all") globals.push(rule.value);
            else rules.splice(index < 0 ? rules.length : index, 0, { ...rule, scope });
            return { ...d, [key]: text(globals), rules };
          })}
        />
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}

function IngressCard({ v }: { v: View }) {
  const save = useSaveFilters();
  const { draft, setDraft, dirty, reset } = useDraft(ingressDraft(v));
  const errors = save.variables?.ingress ? fieldErrors(save.error) : {};
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    save.mutate({ ingress: { enabled: draft.enabled, allow: draft.mode === "allow", networks: splitList(draft.networks) } }, { onSuccess: (nv) => setDraft(ingressDraft(nv)) });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <CardHead title={t("filters.ingress")} sub={t("filters.ingressSub")} />
        <SwitchRow label={t("filters.ingressOn")} sub={t("filters.relay")} checked={draft.enabled} onChange={(enabled) => setDraft((d) => ({ ...d, enabled }))} />
        <Field label={t("filters.mode")} hint={draft.mode === "allow" ? t("filters.allowWarn") : t("filters.denyHint")}>
          <Segmented
            label={t("filters.mode")}
            value={draft.mode}
            onChange={(mode) => setDraft((d) => ({ ...d, mode }))}
            options={[
              { value: "deny", label: t("filters.deny") },
              { value: "allow", label: t("filters.allow") },
            ]}
          />
        </Field>
        <Field label={t("filters.networks")} htmlFor="f-in-networks" hint={t("filters.ingressNetworksHint")} error={listError(errors, "ingress.networks")}>
          <textarea id="f-in-networks" className="input mono min-h-[96px]" value={draft.networks} onChange={(ev) => setDraft((d) => ({ ...d, networks: ev.target.value }))} spellCheck={false} placeholder={"203.0.113.0/24\n2001:db8::/32"} />
        </Field>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}
