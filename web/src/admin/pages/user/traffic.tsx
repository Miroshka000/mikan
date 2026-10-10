import * as Menu from "@radix-ui/react-dropdown-menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorText, unwrap, type User } from "../../../api/client";
import { qk, userActions, useTariffs, useUserMutation, useUserTraffic } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Bar, Button, Ring } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes, dateShort } from "../../../lib/format";
import { PoolLimitsField } from "../pools";
import { tariffSummary } from "../tariffs";
import { LimitPicker } from "./limit-picker";
import { Section, Stat } from "./section";

export function TariffSection({ u }: { u: User }) {
  const tariffs = useTariffs();
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const [pick, setPick] = useState<number | null>(null);
  const current = tariffs.data?.find((x) => x.id === u.tariff_id);
  const next = tariffs.data?.find((x) => x.id === pick);
  return (
    <Section title={t("users.colTariff")}>
      <div className="panel-soft flex items-center justify-between gap-3 p-4">
        <div className="min-w-0">
          <div className="font-display text-base font-medium tracking-tight">{current?.name ?? t("userDrawer.customTerms")}</div>
          <div className="mt-0.5 text-xs text-[var(--ink-500)]">
            {u.traffic_limit != null ? bytes(u.traffic_limit) : t("userDrawer.noTrafficLimit")} · {u.device_limit != null ? t("userDrawer.devicesShort", { n: u.device_limit }) : t("userDrawer.noDeviceLimit")}
            {u.speed_limit != null ? ` · ${t("tariffs.speedShort", { n: u.speed_limit })}` : ""}
            {current?.price_label ? ` · ${current.price_label}` : ""}
          </div>
        </div>
        <Menu.Root>
          <Menu.Trigger asChild>
            <Button variant="ghost" size="sm">
              {t("userDrawer.change")}
            </Button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
              {(tariffs.data ?? []).map((x) => (
                <Menu.Item key={x.id} className="menu-item" onSelect={() => setPick(x.id)}>
                  <span className="flex-1">{x.name}</span>
                  <span className="text-xs font-normal text-[var(--ink-500)]">{tariffSummary(x)}</span>
                </Menu.Item>
              ))}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>
      <SpeedRow u={u} />
      <Confirm
        open={pick !== null}
        onOpenChange={(v) => !v && setPick(null)}
        title={t("userDrawer.switchTitle", { name: next?.name ?? "" })}
        text={t("userDrawer.switchText")}
        confirm={t("userDrawer.switchConfirm")}
        loading={update.isPending}
        onConfirm={() =>
          update.mutate(
            { id: u.id, body: { tariff_id: pick! } },
            {
              onSuccess: () => {
                setPick(null);
                toast.ok(t("userDrawer.tariffChanged"));
              },
              onError: (e) => toast.error(errorText(e)),
            },
          )
        }
      />
    </Section>
  );
}

/** The client's own speed cap; the tariff sets it, here it changes for this client only. */
function SpeedRow({ u }: { u: User }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  return (
    <div className="mt-3">
      <LimitPicker
        label={t("userDrawer.speed")}
        value={u.speed_limit}
        presets={[10, 25, 50, 100]}
        min={1}
        max={100000}
        unit={t("tariffs.mbps")}
        busy={update.isPending}
        onChange={(n, done) =>
          update.mutate({ id: u.id, body: n === null ? { speed_unlimited: true } : { speed_limit: n } }, { onSuccess: done, onError: (e) => toast.error(errorText(e)) })
        }
      />
      <p className="mt-2 text-xs text-[var(--ink-500)]">{t("userDrawer.speedHint")}</p>
    </div>
  );
}

export function TrafficSection({ u }: { u: User }) {
  const traffic = useUserTraffic(u.id);
  const used = u.used_up + u.used_down;
  const pct = u.traffic_limit ? (used / u.traffic_limit) * 100 : 0;
  const pts = traffic.data?.points ?? [];
  const max = Math.max(1, ...pts.map((p) => p.up + p.down));
  return (
    <Section title={t("users.colTraffic")} aside={u.resets_at ? t("userDrawer.resetsOn", { date: dateShort(u.resets_at) }) : t("userDrawer.noReset")}>
      <div className="grid grid-cols-1 items-center gap-5 sm:grid-cols-[112px_minmax(0,1fr)]">
        <Ring
          pct={u.traffic_limit != null ? pct : 100}
          label={u.traffic_limit != null ? `${Math.min(999, Math.round(pct))}%` : "∞"}
          sub={u.traffic_limit != null ? t("users.of", { total: bytes(u.traffic_limit) }) : t("users.unlimited")}
        />
        <div>
          <div className="grid grid-cols-2 gap-x-4 gap-y-3 text-xs text-[var(--ink-500)]">
            <Stat label={t("userDrawer.used")} value={bytes(used)} />
            <Stat label={t("userDrawer.left")} value={u.traffic_limit != null ? bytes(Math.max(0, u.traffic_limit - used) + u.traffic_extra) : "∞"} />
            {u.traffic_limit != null && u.traffic_extra > 0 ? (
              <div className="col-span-2 text-[var(--ink-600)]">{t("grants.plusPackages", { limit: bytes(u.traffic_limit), extra: bytes(u.traffic_extra) })}</div>
            ) : null}
            <Stat label={t("chart.down")} value={bytes(u.used_down)} />
            <Stat label={t("chart.up")} value={bytes(u.used_up)} />
          </div>
          {pts.length > 0 ? (
            <>
              <div className="mt-4 flex h-10 items-end gap-1" aria-hidden>
                {pts.slice(-14).map((p) => (
                  <i key={p.t} className="min-h-[2px] flex-1 rounded-t-[4px] rounded-b-[2px] bg-[var(--mikan-200)] last:bg-[var(--mikan-500)]" style={{ height: `${((p.up + p.down) / max) * 100}%` }} />
                ))}
              </div>
              <div className="mt-1.5 flex justify-between text-[11px] text-[var(--ink-400)]">
                <span>{dateShort(pts.slice(-14)[0]!.t)}</span>
                <span>{t("time.today")}</span>
              </div>
            </>
          ) : null}
        </div>
      </div>
      <div className="mt-3 text-xs text-[var(--ink-500)]">{t("userDrawer.allTime", { bytes: bytes(u.total_up + u.total_down) })}</div>
    </Section>
  );
}

/** Traffic pools of the user: what each one used this period and its limit. */
export function PoolsSection({ u }: { u: User }) {
  const qc = useQueryClient();
  const toast = useToast();
  const pools = useQuery({ queryKey: qk.userPools(u.id), queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/pools", { params: { path: { id: u.id } }, signal })) });
  const [edit, setEdit] = useState<Record<number, string> | null>(null);
  const [closed, setClosed] = useState<Record<number, boolean>>({});
  const tariffs = useTariffs();
  // Access turned back on: the limit of the user's tariff for that pool comes back with it
  // (empty, unlimited, when the tariff has none), not an open pool without a limit.
  const reopen = (next: Record<number, boolean>) => {
    const tariffPools = tariffs.data?.find((x) => x.id === u.tariff_id)?.pools ?? [];
    const back = Object.keys(next).map(Number).filter((id) => closed[id] && !next[id]);
    if (back.length) {
      setEdit((cur) => {
        if (!cur) return cur;
        const filled = { ...cur };
        for (const id of back) {
          const limit = tariffPools.find((p) => p.pool_id === id && !p.excluded)?.traffic_limit;
          if (limit != null && (filled[id] ?? "").trim() === "") filled[id] = String(+(limit / 2 ** 30).toFixed(2));
        }
        return filled;
      });
    }
    setClosed(next);
  };
  const save = useMutation({
    mutationFn: (limits: Record<number, string>) =>
      unwrap(
        api.PUT("/api/v1/users/{id}/pools", {
          params: { path: { id: u.id } },
          body: {
            pools: Object.entries(limits).map(([id, gb]) => {
              if (closed[Number(id)]) return { pool_id: Number(id), traffic_limit: null, excluded: true };
              const v = gb.trim().replace(",", ".");
              return { pool_id: Number(id), traffic_limit: v ? Math.round(Number(v) * 2 ** 30) : null };
            }),
          },
        }),
      ),
    onSuccess: (v) => {
      qc.setQueryData(qk.userPools(u.id), v);
      setEdit(null);
      toast.ok(t("pools.userSaved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  if (!pools.data?.length) return null;
  const bad = edit && Object.entries(edit).some(([id, v]) => !closed[Number(id)] && v.trim() !== "" && !(Number(v.replace(",", ".")) > 0));
  return (
    <Section
      title={t("pools.title")}
      aside={
        edit ? undefined : (
          <button
            type="button"
            className="link-btn text-xs"
            onClick={() => {
              setEdit(Object.fromEntries(pools.data.map((p) => [p.pool_id, p.traffic_limit != null ? String(+(p.traffic_limit / 2 ** 30).toFixed(2)) : ""])));
              setClosed(Object.fromEntries(pools.data.map((p) => [p.pool_id, p.excluded])));
            }}
          >
            {t("pools.editLimits")}
          </button>
        )
      }
    >
      {edit ? (
        <>
          <PoolLimitsField pools={pools.data.map((p) => ({ id: p.pool_id, name: p.name, inbounds: [] }))} value={edit} onChange={setEdit} closed={closed} onClosed={reopen} />
          {bad ? (
            <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
              {t("pools.errLimit")}
            </p>
          ) : null}
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="primary" loading={save.isPending} disabled={!!bad} onClick={() => save.mutate(edit)}>
              {t("common.save")}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </Button>
          </div>
        </>
      ) : (
        <ul className="flex flex-col gap-3">
          {pools.data.map((p) => {
            const used = p.used_up + p.used_down;
            return (
              <li key={p.pool_id}>
                <div className="mb-1 flex items-center justify-between gap-2 text-[13px]">
                  <span className="font-medium">{p.name}</span>
                  <span className="num text-xs text-[var(--ink-600)]">
                    {p.excluded
                      ? t("pools.closed")
                      : p.traffic_limit != null
                      ? `${bytes(used)} ${t("users.of", { total: p.extra > 0 ? t("grants.plusPackages", { limit: bytes(p.traffic_limit), extra: bytes(p.extra) }) : bytes(p.traffic_limit) })}`
                      : `${bytes(used)} · ${t("users.unlimited")}`}
                  </span>
                </div>
                {p.traffic_limit != null && !p.excluded ? <Bar pct={Math.min(100, (used / p.traffic_limit) * 100)} /> : null}
                {p.exhausted ? <div className="mt-1 text-xs text-[var(--berry-600)]">{t("pools.exhausted")}</div> : null}
              </li>
            );
          })}
        </ul>
      )}
    </Section>
  );
}
