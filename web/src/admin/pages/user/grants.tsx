// A user's traffic packages (GitHub issue #12): what was bought or given, what is left,
// and the admin's "Начислить трафик" — a bonus or a compensation.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, unwrap, type Schemas, type User } from "../../../api/client";
import { onFormError } from "../../../lib/fields";
import { qk, usePools, useUserGrants } from "../../../api/hooks";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes, dateShort, days as daysText, GiB } from "../../../lib/format";
import { isLifetimeDays, isTrafficGB, LIFETIME_DAYS_PRESETS, parseDecimal } from "../../../lib/validate";
import { targetName, type Lifetime } from "../packages";

type Grant = Schemas["GrantView"];

export function GrantsSection({ u }: { u: User }) {
  const grants = useUserGrants(u.id);
  const pools = usePools();
  const [adding, setAdding] = useState(false);
  return (
    <section className="dr-sec">
      <h3>
        {t("grants.title")}
        {!adding ? (
          <button type="button" className="link-btn text-xs font-normal" onClick={() => setAdding(true)}>
            <Plus size={14} className="mr-1 inline align-[-2px]" aria-hidden />
            {t("grants.add")}
          </button>
        ) : null}
      </h3>
      {adding ? <GrantForm u={u} pools={pools.data ?? []} onDone={() => setAdding(false)} /> : null}
      <QueryBoundary query={grants} pending={<Skeleton style={{ height: 48, borderRadius: 12 }} />}>
        {(list) =>
          list.length === 0 ? (
            adding ? null : (
              <p className="text-xs text-[var(--ink-500)]">{t("grants.none")}</p>
            )
          ) : (
            <ul className="row-list">
              {list.map((g) => (
                <GrantRow key={g.id} g={g} pools={pools.data} />
              ))}
            </ul>
          )
        }
      </QueryBoundary>
    </section>
  );
}

function GrantRow({ g, pools }: { g: Grant; pools: Schemas["PoolView"][] | undefined }) {
  const expired = !g.active && g.remaining > 0;
  const source = g.source === "purchase" ? t("grants.bought", { name: g.package_name || t("grants.package") }) : t("grants.byAdmin");
  return (
    <li className="py-2">
      <div className="flex items-center justify-between gap-2 text-[13px]">
        <span className="font-medium">
          +{bytes(g.bytes)} · {targetName(g.pool_id, pools)}
        </span>
        {g.active ? (
          <span className="num text-xs text-[var(--ink-600)]">{t("grants.left", { bytes: bytes(g.remaining) })}</span>
        ) : (
          <Pill tone="off">{expired ? t("grants.expired") : t("grants.usedUp")}</Pill>
        )}
      </div>
      <div className="mt-0.5 text-xs text-[var(--ink-500)]">
        {[dateShort(g.created_at), source, g.note, g.expires_at ? t("grants.until", { date: dateShort(g.expires_at) }) : t("grants.noEnd")].filter(Boolean).join(" · ")}
      </div>
    </li>
  );
}

function GrantForm({ u, pools, onDone }: { u: User; pools: Schemas["PoolView"][]; onDone: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [gb, setGb] = useState("10");
  const [pool, setPool] = useState(0);
  const [lifetime, setLifetime] = useState<Lifetime>("used");
  const [days, setDays] = useState("30");
  const [note, setNote] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const grant = useMutation({
    mutationFn: (body: Schemas["GrantInputBody"]) => unwrap(api.POST("/api/v1/users/{id}/grants", { params: { path: { id: u.id } }, body })),
    onSuccess: (g) => {
      // The user's state, traffic and pools change with it.
      void qc.invalidateQueries({ queryKey: qk.users });
      void qc.invalidateQueries({ queryKey: qk.overview });
      toast.ok(t("grants.added", { bytes: bytes(g.bytes) }));
      onDone();
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const gbN = parseDecimal(gb);
    const daysN = Number(days);
    if (!isTrafficGB(gbN)) errs.bytes = t("errors.api.bad_bytes");
    if (lifetime === "days" && !isLifetimeDays(daysN)) errs.days = t("errors.api.bad_days");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    grant.mutate({ bytes: Math.round(gbN * GiB), pool_id: pool || undefined, lifetime, days: lifetime === "days" ? daysN : undefined, note: note.trim() || undefined });
  };
  return (
    <form className="panel-soft mb-3 p-4" onSubmit={submit} noValidate aria-label={t("grants.add")}>
      <div className="grid gap-x-3 sm:grid-cols-2">
        <Field label={t("packages.size")} htmlFor="gr-gb" error={errors.bytes}>
          <div className="flex items-center gap-2">
            <input id="gr-gb" className="input max-w-[120px]" inputMode="decimal" value={gb} onChange={(e) => setGb(e.target.value)} aria-invalid={!!errors.bytes} autoFocus />
            <span className="text-[var(--ink-500)]">{t("units.gb")}</span>
          </div>
        </Field>
        <Field label={t("packages.target")} htmlFor="gr-pool" error={errors.pool_id}>
          <select id="gr-pool" className="input" value={pool} onChange={(e) => setPool(Number(e.target.value))}>
            <option value={0}>{t("pools.mainTraffic")}</option>
            {pools.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field label={t("packages.lifetimeLabel")} htmlFor={lifetime === "days" ? "gr-days" : undefined} hint={t(`packages.lifetimeHint.${lifetime}`)} error={errors.days}>
        <Segmented
          label={t("packages.lifetimeLabel")}
          value={lifetime}
          onChange={setLifetime}
          options={[
            { value: "used", label: t("packages.lifetime.used") },
            { value: "period", label: t("packages.lifetime.period") },
            { value: "days", label: t("packages.lifetimeDays") },
          ]}
        />
        {lifetime === "days" ? (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <input id="gr-days" className="input max-w-[100px]" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} aria-invalid={!!errors.days} />
            {LIFETIME_DAYS_PRESETS.map((n) => (
              <button key={n} type="button" className="chip-btn" onClick={() => setDays(String(n))}>
                {daysText(n)}
              </button>
            ))}
          </div>
        ) : null}
      </Field>
      <Field label={t("grants.note")} htmlFor="gr-note" error={errors.note}>
        <input id="gr-note" className="input" value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} placeholder={t("grants.notePlaceholder")} />
      </Field>
      <div className="flex gap-2">
        <Button size="sm" variant="primary" type="submit" loading={grant.isPending}>
          {t("grants.submit")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onDone} disabled={grant.isPending}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
