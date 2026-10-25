// The form of a tariff: what it gives, its terms and prices, what it includes, and what a
// buyer sees of it in the bot.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Gift, Plus, X } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas, type Tariff } from "../../../api/client";
import { qk, useInbounds, usePackages, usePaymentSettings, usePools } from "../../../api/hooks";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { useFields } from "../../../lib/draft";
import { onFormError } from "../../../lib/fields";
import { bytes, days, GiB, months, termMonths } from "../../../lib/format";
import { isKopecks, isStars, parseDecimal, rubKopecks } from "../../../lib/validate";
import { packageLine } from "../packages";
import { canBuy, fitsTariff, servedGroups, serversHas, type TariffQuotas } from "../sale-rules";
import { draftOf, GroupModesField, settingsOf, type GroupDraft } from "../pools";
import { byNode, serversBody, serversDraftOf, TariffServersTree, type ServersDraft } from "../servers-tree";
import { DeviceModePicker, modeOf, type DeviceMode } from "../user/device-mode";

/** What the server accepts of a description for buyers (domain.CleanDescription). */
const DESCRIPTION_MAX = 300;
const DESCRIPTION_LINES = 5;

/** The tariff form's fields as its inputs hold them. */
type TariffForm = {
  name: string;
  gb: string;
  unlimited: boolean;
  duration: string;
  term: "days" | "day";
  monthsN: string;
  billingDay: string;
  devices: string;
  devicesUnlimited: boolean;
  deviceMode: DeviceMode;
  speed: string;
  speedUnlimited: boolean;
  reset: Tariff["reset_strategy"];
  price: string;
  description: string;
  onSale: boolean;
  // The servers the tariff includes, how it has each server group, and the packages it
  // gives with every payment.
  servers: ServersDraft;
  groups: Record<number, GroupDraft>;
  gifts: Set<number>;
  stars: string;
  rub: string;
  // The terms after the first: days, or months with a billing day, and their prices.
  more: ExtraTerm[];
  // One more device bought in the bot: its price and how many a subscription may buy.
  devMax: string;
  devStars: string;
  devRub: string;
  // The tariff's own part of the lifecycle: the carry-over over the panel's rule, and a term
  // that starts at the first connection.
  carry: Tariff["carry_over"];
  waits: boolean;
};

/** The form of tariff tr; a new one's without. The one place that maps a tariff to the form. */
function formOf(tr: Tariff | null): TariffForm {
  return {
    name: tr?.name ?? "",
    unlimited: tr ? tr.traffic_limit == null : false,
    // Parts of a gigabyte as they are: a round number would save 1.5 GB as 2.
    gb: tr?.traffic_limit != null ? String(Math.round((tr.traffic_limit / GiB) * 100) / 100) : "150",
    duration: String(tr?.duration_days ?? 30),
    term: tr?.billing_day != null ? "day" : "days",
    monthsN: tr?.billing_day != null && !tr.duration_days ? "0" : String(termMonths(tr?.duration_days ?? 30)),
    billingDay: String(tr?.billing_day ?? new Date().getDate()),
    devicesUnlimited: tr ? tr.device_limit == null : false,
    devices: String(tr?.device_limit ?? 3),
    deviceMode: tr ? modeOf(tr) : "bound",
    speedUnlimited: tr?.speed_limit == null,
    speed: String(tr?.speed_limit ?? 100),
    reset: tr?.reset_strategy ?? "period",
    price: tr?.price_label ?? "",
    description: tr?.description ?? "",
    onSale: tr?.on_sale ?? false,
    servers: serversDraftOf(tr?.servers),
    // The view lists every group with the tariff's mode; a new tariff has them all on the main traffic.
    groups: Object.fromEntries((tr?.pools ?? []).map((p) => [p.pool_id, draftOf(p)])),
    gifts: new Set(tr?.gifts ?? []),
    stars: tr?.price_stars != null ? String(tr.price_stars) : "",
    rub: tr?.price_rub != null ? String(tr.price_rub / 100) : "",
    devMax: String(tr?.extra_devices.max ?? 0),
    devStars: tr?.extra_devices.price_stars != null ? String(tr.extra_devices.price_stars) : "",
    devRub: tr?.extra_devices.price_rub != null ? String(tr.extra_devices.price_rub / 100) : "",
    carry: tr?.carry_over ?? "inherit",
    waits: tr?.starts_on_connect ?? false,
    more: (tr?.terms ?? []).slice(1).map((x) => ({
      key: nextKey++,
      days: tr?.billing_day != null ? (x.days ? String(termMonths(x.days)) : "0") : String(x.days),
      stars: x.price_stars != null ? String(x.price_stars) : "",
      rub: x.price_rub != null ? String(x.price_rub / 100) : "",
    })),
  };
}

export function TariffDrawer({ tariff, onClose }: { tariff: Tariff | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [form, set, replace] = useFields(() => formOf(null));
  const { name, gb, unlimited, duration, term, monthsN, billingDay, devices, devicesUnlimited, deviceMode, speed, speedUnlimited, reset, price, description, onSale, servers, groups, gifts, stars, rub, more, devMax, devStars, devRub, carry, waits } = form;
  // A term with an end is one a subscription can wait to start.
  const ends = term === "day" ? Number(monthsN) > 0 : Number(duration) > 0;
  const allPools = usePools();
  // With selling off the sale block is hidden; its values stay as they were.
  const selling = usePaymentSettings().data?.enabled === true;
  const allPackages = usePackages();
  const inbounds = useInbounds();
  const [errors, setErrors] = useState<Record<string, string>>({});
  // What the form gives now: the packages that add to it can be gifts and be bought.
  const draft: TariffQuotas = { trafficLimited: !unlimited, groups, served: inbounds.data ? servedGroups(servers, inbounds.data) : undefined };
  const tariffId = tariff && tariff !== "new" ? tariff.id : 0;
  const giftable = (allPackages.data ?? []).filter((p) => fitsTariff(p, draft));
  const buyable = (allPackages.data ?? []).filter((p) => canBuy(p, fitsTariff(p, draft), tariffId));

  useEffect(() => {
    if (!tariff) return;
    replace(formOf(tariff === "new" ? null : tariff));
    setErrors({});
  }, [tariff, replace]);

  const save = useMutation({
    mutationFn: (body: Schemas["TariffBody"]) =>
      tariff === "new" || !tariff ? unwrap(api.POST("/api/v1/tariffs", { body })) : unwrap(api.PUT("/api/v1/tariffs/{id}", { params: { path: { id: tariff.id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tariffs });
      void qc.invalidateQueries({ queryKey: qk.paymentSettings });
      toast.ok(tariff === "new" ? t("tariffs.created") : t("tariffs.saved"));
      onClose();
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const gbN = Number(gb.replace(",", "."));
    const durN = Number(duration);
    const monN = Number(monthsN);
    const dayN = Number(billingDay);
    const devN = Number(devices);
    const speedN = Number(speed);
    const groupSettings = settingsOf(allPools.data ?? [], groups);
    if (groupSettings.error) errs.pools = groupSettings.error;
    const toDay = term === "day";
    if (!servers.all && servers.nodes.size === 0 && servers.inbounds.size === 0) errs.servers = t("errors.api.servers_empty");
    if (!name.trim()) errs.name = t("tariffs.errName");
    if ([...description.trim()].length > DESCRIPTION_MAX) errs.description = t("errors.api.description_too_long");
    else if (description.trim().split("\n").length > DESCRIPTION_LINES) errs.description = t("errors.api.description_too_many_lines");
    if (!unlimited && (!Number.isFinite(gbN) || gbN <= 0)) errs.traffic_limit = t("tariffs.errTraffic");
    if (!toDay && (!Number.isInteger(durN) || durN < 0 || durN > 3650)) errs.duration_days = t("tariffs.errDuration");
    if (toDay && (!Number.isInteger(monN) || monN < 0 || monN > 120)) errs.duration_days = t("tariffs.errMonths");
    if (toDay && (!Number.isInteger(dayN) || dayN < 1 || dayN > 31)) errs.billing_day = t("tariffs.errBillingDay");
    if (!devicesUnlimited && (!Number.isInteger(devN) || devN < 1 || devN > 100)) errs.device_limit = t("tariffs.errDevices");
    if (!speedUnlimited && (!Number.isInteger(speedN) || speedN < 1 || speedN > 100000)) errs.speed_limit = t("tariffs.errSpeed");
    const starsN = Number(stars);
    const rubN = rubKopecks(rub);
    if (stars.trim() && !isStars(starsN)) errs.price_stars = t("tariffs.errStars");
    if (rub.trim() && !isKopecks(rubN)) errs.price_rub = t("tariffs.errRub");
    if (onSale && !stars.trim() && !rub.trim()) errs.on_sale = t("errors.api.on_sale_no_price");
    const devMaxN = Number(devMax || "0");
    const devStarsN = Number(devStars);
    const devRubN = rubKopecks(devRub);
    if (!Number.isInteger(devMaxN) || devMaxN < 0 || devMaxN > 50) errs["extra_devices.max"] = t("tariffs.extraDevices.errMax");
    if (devStars.trim() && !isStars(devStarsN)) errs["extra_devices.price_stars"] = t("tariffs.errStars");
    if (devRub.trim() && !isKopecks(devRubN)) errs["extra_devices.price_rub"] = t("tariffs.errRub");
    if (devMaxN > 0 && !devStars.trim() && !devRub.trim()) errs["extra_devices.price_rub"] = t("errors.api.extra_devices_price");
    const firstDays = toDay ? monN * 30 : durN;
    const seen = new Set([firstDays]);
    const terms: Schemas["TermBody"][] = [{ days: firstDays, price_stars: stars.trim() ? starsN : undefined, price_rub: rub.trim() ? rubN : undefined }];
    if (more.length && !stars.trim() && !rub.trim()) errs.price_rub = t("errors.api.term_no_price");
    more.forEach((m, i) => {
      const at = `terms[${i + 1}]`;
      const n = Number(m.days);
      const d = toDay ? n * 30 : n;
      if (!Number.isInteger(n) || n < 0 || (toDay ? n > 120 : n > 3650)) errs[`${at}.days`] = t(toDay ? "tariffs.errMonths" : "tariffs.errDuration");
      else if (seen.has(d)) errs[`${at}.days`] = t("errors.api.term_days_repeat");
      seen.add(d);
      const s = Number(m.stars);
      const r = rubKopecks(m.rub);
      if (m.stars.trim() && !isStars(s)) errs[`${at}.price_stars`] = t("tariffs.errStars");
      if (m.rub.trim() && !isKopecks(r)) errs[`${at}.price_rub`] = t("tariffs.errRub");
      if (!m.stars.trim() && !m.rub.trim()) errs[`${at}.price_rub`] = t("errors.api.term_no_price");
      terms.push({ days: d, price_stars: m.stars.trim() ? s : undefined, price_rub: m.rub.trim() ? r : undefined });
    });
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate({
      name: name.trim(),
      traffic_limit: unlimited ? undefined : Math.round(gbN * GiB),
      // The server turns 30 days into one month up to the billing day.
      duration_days: toDay ? monN * 30 : durN,
      billing_day: toDay ? dayN : undefined,
      device_limit: devicesUnlimited ? undefined : devN,
      device_mode: deviceMode,
      speed_limit: speedUnlimited ? undefined : speedN,
      reset_strategy: unlimited ? "none" : reset,
      price_label: price.trim() || undefined,
      description: description.trim() || undefined,
      price_stars: stars.trim() ? starsN : undefined,
      price_rub: rub.trim() ? rubN : undefined,
      terms,
      on_sale: onSale,
      servers: serversBody(servers),
      // What has not loaded is not sent: a missing field keeps the tariff's own, an empty
      // list would wipe its groups and gifts (and open its closed groups to everyone).
      pools: allPools.isSuccess ? groupSettings.settings : undefined,
      // A gift that no longer fits what the form gives (a limit taken off) is not sent.
      gifts: allPackages.isSuccess
        ? [...gifts].filter((id) => {
            const p = allPackages.data.find((x) => x.id === id);
            return p != null && fitsTariff(p, draft);
          })
        : undefined,
      extra_devices: devicesUnlimited
        ? { max: 0, price_stars: null, price_rub: null }
        : { max: devMaxN, price_stars: devStars.trim() ? devStarsN : null, price_rub: devRub.trim() ? devRubN : null },
      // PUT replaces the tariff: keep its place in the list.
      sort: tariff && tariff !== "new" ? tariff.sort : undefined,
      carry_over: carry,
      starts_on_connect: waits && ends,
    });
  };

  return (
    <Drawer
      open={!!tariff}
      onOpenChange={(v) => !v && onClose()}
      title={tariff === "new" ? t("tariffs.new") : t("users.colTariff")}
      meta={t("tariffs.drawerMeta")}
      footer={
        <DrawerFooter form="tariff-form" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="tariff-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("tariffs.name")} htmlFor="t-name" error={errors.name}>
          <input id="t-name" className="input" value={name} onChange={(e) => set.name(e.target.value)} maxLength={60} placeholder={t("tariffs.namePlaceholder")} autoFocus />
        </Field>
        <Field
          label={t("tariffs.description")}
          htmlFor="t-description"
          hint={
            <>
              {t("tariffs.descriptionHint")} <span className="num">{[...description.trim()].length}/{DESCRIPTION_MAX}</span>
            </>
          }
          error={errors.description}
        >
          <textarea id="t-description" className="input min-h-[96px]" rows={3} value={description} onChange={(e) => set.description(e.target.value)} placeholder={t("tariffs.descriptionPlaceholder")} aria-invalid={!!errors.description} />
        </Field>
        <Field label={t("tariffs.trafficPerPeriod")} htmlFor="t-gb" error={errors.traffic_limit}>
          <div className="flex items-center gap-2">
            <input id="t-gb" className="input max-w-[140px]" inputMode="decimal" value={unlimited ? "" : gb} disabled={unlimited} onChange={(e) => set.gb(e.target.value)} aria-invalid={!!errors.traffic_limit} />
            <span className="text-[var(--ink-500)]">{t("units.gb")}</span>
            <label className="ml-auto flex items-center gap-2 text-[13px]">
              <input type="checkbox" className="check" checked={unlimited} onChange={(e) => set.unlimited(e.target.checked)} /> {t("users.unlimited")}
            </label>
          </div>
        </Field>
        <Field label={t("users.colExpiry")} htmlFor={term === "day" ? "t-months" : "t-days"} hint={term === "day" ? t("tariffs.monthsHint") : t("tariffs.durationHint")} error={errors.duration_days}>
          <Segmented
            label={t("tariffs.termKind")}
            value={term}
            onChange={set.term}
            options={[
              { value: "days", label: t("tariffs.termDays") },
              { value: "day", label: t("tariffs.termToDate") },
            ]}
          />
          {term === "day" ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <input id="t-months" className="input max-w-[80px]" inputMode="numeric" value={monthsN} onChange={(e) => set.monthsN(e.target.value)} aria-invalid={!!errors.duration_days} />
              {[1, 3, 6, 12].map((n) => (
                <button key={n} type="button" className="chip-btn" onClick={() => set.monthsN(String(n))}>
                  {n === 12 ? t("userDrawer.year") : months(n)}
                </button>
              ))}
            </div>
          ) : (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <input id="t-days" className="input max-w-[100px]" inputMode="numeric" value={duration} onChange={(e) => set.duration(e.target.value)} aria-invalid={!!errors.duration_days} />
              {[3, 7, 30, 90, 365].map((n) => (
                <button key={n} type="button" className="chip-btn" onClick={() => set.duration(String(n))}>
                  {n === 365 ? t("userDrawer.year") : days(n)}
                </button>
              ))}
            </div>
          )}
        </Field>
        {ends ? <SwitchRow className="mb-4 py-0" label={t("tariffs.waits")} sub={t("tariffs.waitsSub")} checked={waits} onChange={set.waits} /> : null}
        {term === "day" ? (
          <Field label={t("tariffs.billingDay")} htmlFor="t-bday" hint={t("tariffs.billingDayHint")} error={errors.billing_day}>
            <div className="flex items-center gap-2">
              <input id="t-bday" className="input max-w-[100px]" inputMode="numeric" value={billingDay} onChange={(e) => set.billingDay(e.target.value)} aria-invalid={!!errors.billing_day} />
              <span className="text-[var(--ink-500)]">{t("tariffs.dayOfEveryMonth")}</span>
            </div>
          </Field>
        ) : null}
        <Field label={t("tariffs.devicesAtOnce")} htmlFor="t-dev" error={errors.device_limit}>
          <div className="flex items-center gap-2">
            <input id="t-dev" className="input max-w-[100px]" inputMode="numeric" value={devicesUnlimited ? "" : devices} disabled={devicesUnlimited} onChange={(e) => set.devices(e.target.value)} />
            <label className="ml-auto flex items-center gap-2 text-[13px]">
              <input type="checkbox" className="check" checked={devicesUnlimited} onChange={(e) => set.devicesUnlimited(e.target.checked)} /> {t("users.unlimited")}
            </label>
          </div>
        </Field>
        {devicesUnlimited ? null : (
          <Field label={t("deviceMode.label")}>
            <DeviceModePicker value={deviceMode} onChange={set.deviceMode} />
          </Field>
        )}
        <Field label={t("tariffs.speed")} htmlFor="t-speed" hint={t("tariffs.speedHint")} error={errors.speed_limit}>
          <div className="flex items-center gap-2">
            <label className="input-unit max-w-[160px] flex-1">
              <input id="t-speed" className="input" inputMode="numeric" value={speedUnlimited ? "" : speed} disabled={speedUnlimited} onChange={(e) => set.speed(e.target.value)} aria-invalid={!!errors.speed_limit} />
              <span>{t("tariffs.mbps")}</span>
            </label>
            <label className="ml-auto flex items-center gap-2 text-[13px]">
              <input type="checkbox" className="check" checked={speedUnlimited} onChange={(e) => set.speedUnlimited(e.target.checked)} /> {t("users.unlimited")}
            </label>
          </div>
        </Field>
        {!unlimited ? (
          <Field label={t("tariffs.whenReset")} hint={t("tariffs.whenResetHint")}>
            <Segmented
              label={t("tariffs.reset")}
              value={reset}
              onChange={set.reset}
              options={[
                { value: "period", label: t("tariffs.resetEvery30") },
                { value: "month_start", label: term === "day" && Number(billingDay) >= 1 && Number(billingDay) <= 31 ? t("tariffs.resetOnDayShort", { d: Number(billingDay) }) : t("tariffs.resetMonthStart") },
                { value: "none", label: t("tariffs.resetNever") },
              ]}
            />
          </Field>
        ) : null}
        {!unlimited ? (
          <Field label={t("tariffs.carry")} hint={t("tariffs.carryHint")}>
            <Segmented
              label={t("tariffs.carry")}
              value={carry}
              onChange={set.carry}
              options={[
                { value: "inherit", label: t("tariffs.carryInherit") },
                { value: "on", label: t("tariffs.carryOn") },
                { value: "off", label: t("tariffs.carryOff") },
              ]}
            />
          </Field>
        ) : null}
        <div className="border-t border-[var(--hairline)] pt-4" role="group" aria-labelledby="t-contents">
          <div className="mb-3">
            <div id="t-contents" className="text-[13px] font-semibold">
              {t("tariffs.contents")}
            </div>
            <div className="text-xs text-[var(--ink-500)]">{t("tariffs.contentsSub")}</div>
          </div>
          <Field label={t("servers.label")} hint={t("servers.hint")} error={errors.servers ?? errors["servers.nodes"] ?? errors["servers.inbounds"]}>
            <Segmented
              label={t("servers.label")}
              value={servers.all ? "all" : "chosen"}
              onChange={(v) => set.servers({ ...servers, all: v === "all" })}
              options={[
                { value: "all", label: t("servers.all") },
                { value: "chosen", label: t("servers.chosen") },
              ]}
            />
            {servers.all ? null : inbounds.isPending ? (
              <Skeleton className="mt-2" style={{ height: 96 }} />
            ) : (inbounds.data ?? []).length === 0 ? (
              <p className="mt-2 text-xs text-[var(--ink-500)]">{t("pools.noInbounds")}</p>
            ) : (
              <div className="mt-2">
                <TariffServersTree nodes={byNode(inbounds.data)} value={servers} onChange={set.servers} />
              </div>
            )}
          </Field>
          {allPools.isPending ? (
            <Field label={t("pools.tariffLimits")}>
              <Skeleton style={{ height: 88 }} />
            </Field>
          ) : allPools.isError ? (
            <Field label={t("pools.tariffLimits")}>
              <p className="text-xs text-[var(--berry-600)]" role="alert">
                {errorText(allPools.error)}
              </p>
            </Field>
          ) : allPools.data.length ? (
            <Field label={t("pools.tariffLimits")} hint={`${t("pools.tariffLimitsHint")} ${t("pools.closeHint")}`} error={errors.pools}>
              <GroupModesField groups={allPools.data} value={groups} onChange={set.groups} />
            </Field>
          ) : null}
          <Field label={t("tariffs.gifts")} hint={t("tariffs.giftsHint")} error={errors.gifts}>
            {allPackages.isPending ? (
              <Skeleton style={{ height: 44 }} />
            ) : (allPackages.data ?? []).length === 0 ? (
              <p className="text-xs text-[var(--ink-500)]">{t("tariffs.giftsNone")}</p>
            ) : (
              <ul className="row-list rounded-2xl border border-[var(--hairline)] px-3">
                {(allPackages.data ?? []).map((p) => {
                  const fits = giftable.includes(p);
                  return (
                    <li key={p.id}>
                      <label className={`flex items-center gap-2 py-2 text-[13px] ${fits ? "" : "text-[var(--ink-400)]"}`}>
                        <input
                          type="checkbox"
                          className="check"
                          checked={fits && gifts.has(p.id)}
                          disabled={!fits}
                          aria-label={t("tariffs.giftOf", { name: p.name })}
                          onChange={(e) => {
                            const next = new Set(gifts);
                            if (e.target.checked) next.add(p.id);
                            else next.delete(p.id);
                            set.gifts(next);
                          }}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="font-medium">{p.name}</span>
                          <span className="block truncate text-xs text-[var(--ink-500)]">{packageLine(p, allPools.data)}</span>
                        </span>
                        {fits ? null : <span className="text-xs">{t("packages.notFit")}</span>}
                      </label>
                    </li>
                  );
                })}
              </ul>
            )}
          </Field>
          <Field label={t("tariffs.buyable")}>
            {buyable.length ? (
              <ul className="flex flex-wrap gap-1.5">
                {buyable.map((p) => (
                  <li key={p.id}>
                    <Pill tone="ok">
                      {p.name} · {bytes(p.bytes)}
                    </Pill>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-xs text-[var(--ink-500)]">{t("tariffs.buyableNone")}</p>
            )}
          </Field>
          <TariffPreview
            name={name.trim() || t("tariffs.namePlaceholder")}
            traffic={unlimited ? null : Math.round(Number(gb.replace(",", ".")) * GiB) || null}
            devices={devicesUnlimited ? null : Number(devices) || null}
            online={deviceMode === "online"}
            speed={speedUnlimited ? null : Number(speed) || null}
            groups={(allPools.data ?? []).map((g) => ({
              name: g.name,
              draft: groups[g.id] ?? draftOf(undefined),
              // A group shows when the tariff includes a server of it.
              // As the bot shows it (billing.contents): a group with a protocol that is on.
              serving: (inbounds.data ?? []).some((i) => i.enabled && g.inbound_ids.includes(i.id) && serversHas(servers, i)),
            }))}
            gifts={(allPackages.data ?? []).filter((p) => gifts.has(p.id) && giftable.includes(p)).map((p) => packageLine(p, allPools.data))}
            description={description.trim()}
          />
        </div>
        <div className="border-t border-[var(--hairline)] pt-4" role="group" aria-label={t("tariffs.sale")}>
          {selling ? (
            <>
              <SwitchRow className="mb-3 py-0" title={t("tariffs.sale")} label={t("tariffs.onSale")} sub={t("tariffs.saleSub")} checked={onSale} onChange={set.onSale} />
              {errors.on_sale ? (
                <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
                  {errors.on_sale}
                </p>
              ) : null}
              <div className="grid gap-x-3 sm:grid-cols-2">
                <Field label={t("tariffs.priceStars")} htmlFor="t-stars" hint={t("tariffs.priceStarsHint")} error={errors.price_stars}>
                  <div className="flex items-center gap-2">
                    <input id="t-stars" className="input max-w-[140px]" inputMode="numeric" value={stars} onChange={(e) => set.stars(e.target.value)} placeholder="150" aria-invalid={!!errors.price_stars} />
                    <span className="text-[var(--ink-500)]">⭐</span>
                  </div>
                </Field>
                <Field label={t("tariffs.priceRub")} htmlFor="t-rub" hint={t("tariffs.priceRubHint")} error={errors.price_rub}>
                  <div className="flex items-center gap-2">
                    <input id="t-rub" className="input max-w-[140px]" inputMode="decimal" value={rub} onChange={(e) => set.rub(e.target.value)} placeholder="199" aria-invalid={!!errors.price_rub} />
                    <span className="text-[var(--ink-500)]">₽</span>
                  </div>
                </Field>
              </div>
              <MoreTerms value={more} onChange={set.more} toDay={term === "day"} errors={errors} />
              {devicesUnlimited ? null : (
                <div className="mb-4" role="group" aria-label={t("tariffs.extraDevices.title")}>
                  <div className="text-[13px] font-semibold">{t("tariffs.extraDevices.title")}</div>
                  <div className="mb-2 text-xs text-[var(--ink-500)]">{t("tariffs.extraDevices.sub")}</div>
                  <div className="grid gap-x-3 sm:grid-cols-3">
                    <Field label={t("tariffs.extraDevices.max")} htmlFor="t-dev-max" error={errors["extra_devices.max"]}>
                      <input id="t-dev-max" className="input max-w-[100px]" inputMode="numeric" value={devMax} onChange={(e) => set.devMax(e.target.value)} aria-invalid={!!errors["extra_devices.max"]} />
                    </Field>
                    <Field label={t("tariffs.extraDevices.stars")} htmlFor="t-dev-stars" error={errors["extra_devices.price_stars"]}>
                      <div className="flex items-center gap-2">
                        <input id="t-dev-stars" className="input max-w-[120px]" inputMode="numeric" value={devStars} onChange={(e) => set.devStars(e.target.value)} placeholder="40" aria-invalid={!!errors["extra_devices.price_stars"]} />
                        <span className="text-[var(--ink-500)]">⭐</span>
                      </div>
                    </Field>
                    <Field label={t("tariffs.extraDevices.rub")} htmlFor="t-dev-rub" error={errors["extra_devices.price_rub"]}>
                      <div className="flex items-center gap-2">
                        <input id="t-dev-rub" className="input max-w-[120px]" inputMode="decimal" value={devRub} onChange={(e) => set.devRub(e.target.value)} placeholder="49" aria-invalid={!!errors["extra_devices.price_rub"]} />
                        <span className="text-[var(--ink-500)]">₽</span>
                      </div>
                    </Field>
                  </div>
                </div>
              )}
            </>
          ) : null}
          <Field label={t("tariffs.price")} htmlFor="t-price" hint={t("tariffs.priceHint")}>
            <input id="t-price" className="input" value={price} onChange={(e) => set.price(e.target.value)} maxLength={40} placeholder={t("tariffs.pricePlaceholder")} />
          </Field>
        </div>
      </form>
    </Drawer>
  );
}

type ExtraTerm = { key: number; days: string; stars: string; rub: string };

let nextKey = 0;

/** The terms a tariff is sold for after its first: each its own days (or months up to the
 * billing day) and prices. The bot and the Mini App let the buyer pick one. */
function MoreTerms({ value, onChange, toDay, errors }: { value: ExtraTerm[]; onChange: (v: ExtraTerm[]) => void; toDay: boolean; errors: Record<string, string> }) {
  const set = (i: number, k: "days" | "stars" | "rub", v: string) => onChange(value.map((m, j) => (j === i ? { ...m, [k]: v } : m)));
  return (
    <div className="mb-4" role="group" aria-label={t("tariffs.moreTerms")}>
      <div className="text-[13px] font-semibold">{t("tariffs.moreTerms")}</div>
      <div className="mb-2 text-xs text-[var(--ink-500)]">{t("tariffs.moreTermsSub")}</div>
      {value.map((m, i) => {
        const at = `terms[${i + 1}]`;
        const err = errors[`${at}.days`] ?? errors[`${at}.price_stars`] ?? errors[`${at}.price_rub`];
        return (
          <div key={m.key} className="mb-2">
            <div className="grid grid-cols-[1fr_1fr_1fr_auto] items-center gap-2">
              <label className="input-unit min-w-0">
                <input
                  className="input num"
                  inputMode="numeric"
                  value={m.days}
                  onChange={(e) => set(i, "days", e.target.value)}
                  aria-label={t(toDay ? "tariffs.termMonthsLabel" : "tariffs.termDaysLabel", { n: i + 2 })}
                  aria-invalid={!!errors[`${at}.days`]}
                />
                <span>{t(toDay ? "tariffs.monthsShort" : "tariffs.daysShort")}</span>
              </label>
              <label className="input-unit min-w-0">
                <input className="input num" inputMode="numeric" value={m.stars} onChange={(e) => set(i, "stars", e.target.value)} aria-label={t("tariffs.priceStars")} aria-invalid={!!errors[`${at}.price_stars`]} />
                <span>⭐</span>
              </label>
              <label className="input-unit min-w-0">
                <input className="input num" inputMode="decimal" value={m.rub} onChange={(e) => set(i, "rub", e.target.value)} aria-label={t("tariffs.priceRub")} aria-invalid={!!errors[`${at}.price_rub`]} />
                <span>₽</span>
              </label>
              <button type="button" className="icon-btn" aria-label={t("tariffs.removeTerm")} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <X size={16} />
              </button>
            </div>
            {err ? (
              <p className="mt-1 text-xs text-[var(--berry-600)]" role="alert">
                {err}
              </p>
            ) : null}
          </div>
        );
      })}
      {value.length < 11 ? (
        <Button type="button" size="sm" variant="ghost" onClick={() => onChange([...value, { key: nextKey++, days: "", stars: "", rub: "" }])}>
          <Plus size={16} aria-hidden /> {t("tariffs.addTerm")}
        </Button>
      ) : null}
    </div>
  );
}

/** The tariff as the buyer reads it in the bot and the Mini App: the line of its limits, its
 * server groups (the ones with servers in them), its gifts and the admin's description. */
function TariffPreview({
  name,
  traffic,
  devices,
  online,
  speed,
  groups,
  gifts,
  description,
}: {
  name: string;
  traffic: number | null;
  devices: number | null;
  online: boolean;
  speed: number | null;
  groups: { name: string; draft: GroupDraft; serving: boolean }[];
  gifts: string[];
  description: string;
}) {
  const limits = [
    traffic != null ? bytes(traffic) : t("users.unlimited"),
    ...(devices != null ? [t(online ? "deviceMode.shortOnline" : "userDrawer.devicesShort", { n: devices })] : []),
    ...(speed != null ? [t("tariffs.speedShort", { n: speed })] : []),
  ].join(" · ");
  const line = (g: { name: string; draft: GroupDraft }) => {
    switch (g.draft.mode) {
      case "own": {
        const gb = parseDecimal(g.draft.gb.trim());
        return t("tariffs.groupOwn", { name: g.name, gb: gb > 0 ? bytes(Math.round(gb * GiB)) : "—" });
      }
      case "unlimited":
        return t("tariffs.groupUnlimited", { name: g.name });
      case "closed":
        return t("tariffs.groupClosed", { name: g.name });
      default:
        return t("tariffs.groupIn", { name: g.name });
    }
  };
  const shown = groups.filter((g) => g.serving);
  return (
    <section className="panel-soft p-4" aria-labelledby="t-preview">
      <div id="t-preview" className="text-[13px] font-semibold">
        {t("tariffs.preview")}
      </div>
      <div className="mb-3 text-xs text-[var(--ink-500)]">{t("tariffs.previewHint")}</div>
      <div className="rounded-2xl border border-[var(--hairline)] bg-[var(--surface)] p-3">
        <div className="text-[13px] font-semibold">{name}</div>
        <div className="text-xs text-[var(--ink-600)]">{limits}</div>
        {shown.length || gifts.length ? (
          <ul className="mt-2 flex flex-col gap-1 text-xs text-[var(--ink-700)]">
            {shown.map((g) => (
              <li key={g.name} className="flex items-center gap-1.5">
                {g.draft.mode === "closed" ? <X size={14} className="shrink-0 text-[var(--ink-400)]" aria-hidden /> : <Check size={14} className="shrink-0 text-[var(--leaf-500)]" aria-hidden />}
                {line(g)}
              </li>
            ))}
            {gifts.map((what, i) => (
              <li key={i} className="flex items-center gap-1.5">
                <Gift size={14} className="shrink-0 text-[var(--mikan-600)]" aria-hidden />
                {t("tariffs.previewGift", { what })}
              </li>
            ))}
          </ul>
        ) : null}
        {description ? <p className="mt-2 whitespace-pre-line text-xs text-[var(--ink-600)]">{description}</p> : null}
      </div>
    </section>
  );
}
