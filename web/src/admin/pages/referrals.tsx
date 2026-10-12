import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, rawJson, useTariffs } from "../../api/hooks";
import { QueryBoundary } from "../../components/query";
import { Switch, SwitchRow } from "../../components/switch";
import { FormActions } from "../../components/layout";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { dateShort, money, num } from "../../lib/format";

type Rule = Schemas["Rule"];
type View = Schemas["ReferralView"];
type Reward = Schemas["ReferralRewardView"];
type Link = Schemas["ReferralLinkView"];

type Kind = Rule["kind"];
const KIND_LABEL: Record<Kind, "referrals.kindPercent" | "referrals.kindDays"> = { percent: "referrals.kindPercent", days: "referrals.kindDays" };

/** The setup with its numbers; the rules come with the ids the editor keeps across a save. */
function blankRule(n: number): Rule {
  return { id: `r${n}`, name: "", kind: "percent", value: 10, every: 1, tariff_ids: [], enabled: true };
}

export function useReferral() {
  return useQuery({
    queryKey: qk.referrals,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/referrals", { signal })),
  });
}

/** What the programme has paid out, and who was invited by whom. */
function useReferralLists(enabled: boolean) {
  return {
    rewards: useQuery({
      queryKey: [...qk.referrals, "rewards"],
      enabled,
      queryFn: ({ signal }) => rawJson<{ items: Reward[]; total: number }>("/api/v1/referrals/rewards?limit=50", { signal }),
    }),
    links: useQuery({
      queryKey: [...qk.referrals, "links"],
      enabled,
      queryFn: ({ signal }) => rawJson<{ items: Link[]; total: number }>("/api/v1/referrals/links?limit=50", { signal }),
    }),
  };
}

export function Referrals() {
  const view = useReferral();
  return (
    <QueryBoundary query={view} pending={<Skeleton style={{ height: 360, borderRadius: 20 }} />}>
      {(v) => <ReferralCards v={v} />}
    </QueryBoundary>
  );
}

function ReferralCards({ v }: { v: View }) {
  const { rewards, links } = useReferralLists(true);
  return (
    <div className="flex flex-col gap-4">
      <SetupCard v={v} />
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] xl:items-start">
        <RewardsCard rewards={rewards} />
        <LinksCard links={links} />
      </div>
    </div>
  );
}

function SetupCard({ v }: { v: View }) {
  const tariffs = useTariffs();
  const qc = useQueryClient();
  const toast = useToast();
  const save = useMutation({
    mutationFn: (body: Schemas["Program"]) => unwrap(api.PATCH("/api/v1/referrals", { body })),
    onSuccess: (next) => {
      qc.setQueryData(qk.referrals, next);
      void qc.invalidateQueries({ queryKey: [...qk.referrals, "rewards"] });
      void qc.invalidateQueries({ queryKey: [...qk.referrals, "links"] });
      toast.ok(t("referrals.saved"));
    },
  });
  const { draft: f, setDraft: setF, dirty, reset } = useDraft({
    enabled: v.config.enabled,
    starsRate: v.config.stars_rate,
    balanceMax: v.config.balance_max,
    spend: v.config.spend,
    notify: v.config.notify,
    // A programme with no rules at all still needs a row to fill in.
    rules: v.config.rules.length ? v.config.rules : [blankRule(1)],
  });
  const patch = (id: string, next: Partial<Rule>) => setF((cur) => ({ ...cur, rules: cur.rules.map((r) => (r.id === id ? { ...r, ...next } : r)) }));
  const remove = (id: string) => setF((cur) => ({ ...cur, rules: cur.rules.filter((r) => r.id !== id) }));
  const add = () =>
    setF((cur) => {
      let n = cur.rules.length + 1;
      while (cur.rules.some((r) => r.id === `r${n}`)) n++;
      return { ...cur, rules: [...cur.rules, blankRule(n)] };
    });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({
      enabled: f.enabled,
      stars_rate: f.starsRate,
      balance_max: f.balanceMax,
      spend: f.spend,
      notify: f.notify,
      rules: f.rules.map((r, i) => ({ ...r, id: r.id || `r${i + 1}` })),
    });
  };
  const nameOf = (id: number) => (tariffs.data ?? []).find((tr) => tr.id === id)?.name ?? `#${id}`;
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div className="min-w-0">
            <h2 className="card-title">{t("referrals.title")}</h2>
            <div className="card-sub">{t("referrals.subtitle")}</div>
          </div>
          <Stats stats={v.stats} />
        </div>
        {save.error ? <div className="banner err mb-4">{errorText(save.error)}</div> : null}

        <div className="row-list">
          <SwitchRow label={t("referrals.on")} sub={t("referrals.onSub")} checked={f.enabled} onChange={(x) => setF((c) => ({ ...c, enabled: x }))} />
          <SwitchRow label={t("referrals.spend")} sub={t("referrals.spendSub")} checked={f.spend} onChange={(x) => setF((c) => ({ ...c, spend: x }))} />
          <SwitchRow label={t("referrals.notify")} sub={t("referrals.notifySub")} checked={f.notify} onChange={(x) => setF((c) => ({ ...c, notify: x }))} />
          <div className="grid grid-cols-1 gap-3 pt-3 sm:grid-cols-2">
            <Field label={t("referrals.starsRate")} hint={t("referrals.starsRateHint")}>
              <input
                className="input"
                type="number"
                min={0}
                step={1}
                value={f.starsRate}
                onChange={(e) => setF((c) => ({ ...c, starsRate: Math.max(0, Number(e.target.value) || 0) }))}
              />
            </Field>
            <Field label={t("referrals.balanceMax")} hint={t("referrals.balanceMaxHint")}>
              <input
                className="input"
                type="number"
                min={0}
                step={100}
                value={f.balanceMax}
                onChange={(e) => setF((c) => ({ ...c, balanceMax: Math.max(0, Number(e.target.value) || 0) }))}
              />
            </Field>
          </div>
        </div>

        <div className="mt-5">
          <div className="mb-2 flex flex-wrap items-center justify-between gap-3">
            <h3 className="card-title">{t("referrals.rules")}</h3>
            <Button type="button" size="sm" onClick={add}>
              <Plus size={16} aria-hidden /> {t("referrals.addRule")}
            </Button>
          </div>
          <p className="mb-3 text-xs text-[var(--ink-500)]">{t("referrals.rulesHint")}</p>
          {f.rules.length === 0 ? <p className="text-sm text-[var(--ink-500)]">{t("referrals.noRules")}</p> : null}
          <ul className="flex flex-col gap-3">
            {f.rules.map((r, i) => (
              <li key={r.id} className="rounded-[var(--r-md)] border border-[var(--hairline)] bg-[var(--surface)] p-3">
                <div className="mb-2 flex items-center justify-between gap-3">
                  <Switch checked={r.enabled} onChange={(x) => patch(r.id, { enabled: x })} label={t("referrals.ruleOn", { n: i + 1 })} />
                  <Button type="button" size="sm" variant="ghost" onClick={() => remove(r.id)} aria-label={t("referrals.removeRule", { n: i + 1 })}>
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </div>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  <Field label={t("referrals.kind")}>
                    <select className="input" value={r.kind} onChange={(e) => patch(r.id, { kind: e.target.value as Kind })}>
                      <option value="percent">{t("referrals.kindPercent")}</option>
                      <option value="days">{t("referrals.kindDays")}</option>
                    </select>
                  </Field>
                  <Field label={r.kind === "percent" ? t("referrals.percent") : t("referrals.dayCount")}>
                    <input
                      className="input"
                      type="number"
                      min={1}
                      max={r.kind === "percent" ? 100 : 3650}
                      value={r.value}
                      onChange={(e) => patch(r.id, { value: Math.max(1, Number(e.target.value) || 1) })}
                    />
                  </Field>
                  <Field label={r.kind === "percent" ? t("referrals.every") : t("referrals.everyCount")}>
                    <input
                      className="input"
                      type="number"
                      min={1}
                      max={10000}
                      value={r.every}
                      onChange={(e) => patch(r.id, { every: Math.max(1, Number(e.target.value) || 1) })}
                    />
                  </Field>
                  <Field label={t("referrals.name")} hint={t("referrals.nameHint")}>
                    <input className="input" maxLength={80} value={r.name} onChange={(e) => patch(r.id, { name: e.target.value })} />
                  </Field>
                </div>
                <div className="mt-3">
                  <TariffPicker value={r.tariff_ids} onChange={(ids) => patch(r.id, { tariff_ids: ids })} nameOf={nameOf} />
                </div>
                <p className="mt-2 text-xs text-[var(--ink-500)]">{describe(r)}</p>
              </li>
            ))}
          </ul>
        </div>

        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} note={t("referrals.note")} />
      </form>
    </section>
  );
}

/** What a rule will pay, in the admin's own words. */
function describe(r: Rule): string {
  if (r.kind === "percent") {
    return r.every > 1 ? t("referrals.percentEvery", { n: r.value, every: r.every }) : t("referrals.percentEach", { n: r.value });
  }
  return r.every > 1 ? t("referrals.daysEvery", { n: r.value, every: r.every }) : t("referrals.daysEach", { n: r.value });
}

/** The tariffs a rule counts. None picked means any, which is said out loud. */
function TariffPicker({ value, onChange, nameOf }: { value: number[]; onChange: (v: number[]) => void; nameOf: (id: number) => string }) {
  const tariffs = useTariffs();
  const toggle = (id: number) => onChange(value.includes(id) ? value.filter((x) => x !== id) : [...value, id]);
  return (
    <div>
      <div className="mb-1 text-xs text-[var(--ink-500)]">{t("referrals.tariffs")}</div>
      <div className="flex flex-wrap gap-2" role="group" aria-label={t("referrals.tariffs")}>
        {(tariffs.data ?? []).map((tr) => (
          <button key={tr.id} type="button" className="chip-btn" aria-pressed={value.includes(tr.id)} onClick={() => toggle(tr.id)}>
            {nameOf(tr.id)}
          </button>
        ))}
      </div>
      {value.length === 0 ? <p className="mt-1 text-xs text-[var(--ink-500)]">{t("referrals.anyTariff")}</p> : null}
    </div>
  );
}

function Stats({ stats }: { stats: View["stats"] }) {
  const items: [string, string][] = [
    [t("referrals.statInvited"), num(stats.invited)],
    [t("referrals.statPaid"), num(stats.paid)],
    [t("referrals.statEarned"), money(stats.earned, "RUB")],
    [t("referrals.statSpent"), money(stats.spent, "RUB")],
    [t("referrals.statOwed"), money(stats.owed, "RUB")],
  ];
  return (
    <dl className="flex flex-wrap gap-x-5 gap-y-1" aria-label={t("referrals.stats")}>
      {items.map(([label, value]) => (
        <div key={label}>
          <dt className="text-[11px] text-[var(--ink-500)]">{label}</dt>
          <dd className="num text-[15px] font-semibold">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function RewardsCard({ rewards }: { rewards: ReturnType<typeof useReferralLists>["rewards"] }) {
  const items = rewards.data?.items ?? [];
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("referrals.rewardsTitle")}</h2>
          <div className="card-sub">{t("referrals.rewardsSub")}</div>
        </div>
      </div>
      {rewards.isError ? (
        <ErrorState text={errorText(rewards.error)} onRetry={() => void rewards.refetch()} />
      ) : rewards.isPending ? (
        <Skeleton style={{ height: 140 }} />
      ) : items.length === 0 ? (
        <p className="text-sm text-[var(--ink-500)]">{t("referrals.rewardsEmpty")}</p>
      ) : (
        <ul className="row-list">
          {items.map((r) => (
            <li key={r.id} className="flex items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <b className="num">{r.kind === "days" ? t("referrals.daysShort", { n: r.days }) : money(r.amount, "RUB")}</b>
                  <Pill tone={r.reverted_at ? "off" : "ok"}>{r.reverted_at ? t("referrals.reverted") : t(KIND_LABEL[r.kind])}</Pill>
                  <span className="truncate text-[var(--ink-500)]">{r.rule_id}</span>
                </div>
                <div className="mt-0.5 truncate text-xs text-[var(--ink-500)]">
                  {r.referrer_name || `tg ${r.referrer_tg_id}`} · {dateShort(r.created_at)} · {t("referrals.everyShort", { n: r.every_n })}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function LinksCard({ links }: { links: ReturnType<typeof useReferralLists>["links"] }) {
  const items = links.data?.items ?? [];
  const [showAll, setShowAll] = useState(false);
  const shown = showAll ? items : items.slice(0, 10);
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("referrals.linksTitle")}</h2>
          <div className="card-sub">{t("referrals.linksSub")}</div>
        </div>
      </div>
      {links.isError ? (
        <ErrorState text={errorText(links.error)} onRetry={() => void links.refetch()} />
      ) : links.isPending ? (
        <Skeleton style={{ height: 140 }} />
      ) : items.length === 0 ? (
        <EmptyState title={t("referrals.linksEmpty")} text={t("referrals.linksEmptyText")} />
      ) : (
        <>
          <ul className="row-list">
            {shown.map((l) => (
              <li key={l.referral_id} className="flex items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <div className="truncate text-[13px]">
                    {l.referee_name ? `@${l.referee_name}` : `tg ${l.referee_tg_id}`} <span className="text-[var(--ink-500)]">{t("referrals.invitedBy", { code: l.referrer_code })}</span>
                  </div>
                  <div className="mt-0.5 text-xs text-[var(--ink-500)]">{dateShort(l.created_at)}</div>
                </div>
                <Pill tone={l.first_paid_at ? "ok" : "off"}>{l.first_paid_at ? t("referrals.paid") : t("referrals.invited")}</Pill>
              </li>
            ))}
          </ul>
          {items.length > 10 ? (
            <Button className="mt-3" size="sm" onClick={() => setShowAll((v) => !v)}>
              {showAll ? t("referrals.showLess") : t("referrals.showMore", { n: items.length })}
            </Button>
          ) : null}
        </>
      )}
    </section>
  );
}