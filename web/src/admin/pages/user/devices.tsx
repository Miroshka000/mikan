import { Ban, Hourglass, Laptop, Layers, Pencil, Smartphone, Unlink } from "lucide-react";
import { useState } from "react";
import { ApiError, errorText, type Schemas, type User } from "../../../api/client";
import { useBoundDevices, useDeviceBans, useDevices, userActions, useSettings, useUserMutation } from "../../../api/hooks";
import { Confirm, NameDialog } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { DEVICE_NAME_MAX, desktopOS, deviceDetails, deviceLabel, reportedName } from "../../../lib/devices";
import { ago, bytes, dateShort, maskIP, time } from "../../../lib/format";
import { LimitPicker } from "./limit-picker";
import { Section } from "./section";

export function DevicesSection({ u }: { u: User }) {
  const devices = useDevices(u.id);
  // With binding a place is a bound device; addresses are only where they connect from.
  const binding = !!useSettings().data?.device_binding;
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const list = devices.data ?? [];
  return (
    <Section title={t("userDrawer.devices")} aside={
        binding
          ? t("userDrawer.devicesAsideBound", { used: u.bound_devices, limit: u.device_limit ?? "∞", online: u.online_ips.length })
          : t("userDrawer.devicesAside", { online: u.online_ips.length, limit: u.device_limit ?? "∞" })
      }>
      <div className="mb-4">
        <LimitPicker
          label={t("userDrawer.deviceLimit")}
          value={u.device_limit}
          presets={[1, 2, 3, 5, 10]}
          min={1}
          max={100}
          busy={update.isPending}
          onChange={(n, done) =>
            update.mutate({ id: u.id, body: n === null ? { devices_unlimited: true } : { device_limit: n } }, { onSuccess: done, onError: (e) => toast.error(errorText(e)) })
          }
        />
      </div>
      <BoundDevices u={u} />
      <h4 className="mt-5 mb-2 text-xs font-medium text-[var(--ink-500)]">{t("userDrawer.addresses")}</h4>
      {devices.isPending ? (
        <Skeleton style={{ height: 52, borderRadius: 16 }} />
      ) : devices.data === undefined ? (
        <ErrorState text={errorText(devices.error)} onRetry={() => void devices.refetch()} />
      ) : list.length === 0 ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.noDevices")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {list.slice(0, 8).map((d) => (
            <li key={d.ip} className="panel-soft grid grid-cols-[36px_minmax(0,1fr)] items-center gap-3 p-2">
              <span className="grid h-9 w-9 place-items-center rounded-[10px] bg-[var(--hover)] text-[var(--ink-600)]"><Smartphone size={18} /></span>
              <div className="min-w-0">
                <div className="text-[13px] font-medium">{binding ? t("userDrawer.address") : t("userDrawer.device")}</div>
                <div className="text-xs text-[var(--ink-500)]">
                  <span className="mono">{maskIP(d.ip)}</span> · {d.online ? <span className="text-[var(--leaf-700)]">{t("users.onlineNow")}</span> : ago(d.last_seen)}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 text-xs text-[var(--ink-500)]">{binding ? t("userDrawer.devicesNoteBound") : t("userDrawer.devicesNote")}</p>
    </Section>
  );
}

type BoundDevice = Schemas["BoundDeviceView"];
type DeviceBan = Schemas["DeviceBanView"];

/** What to call a bound device: its own name, else what its app reported. */
function deviceName(d: BoundDevice): string {
  return d.hwid || d.name ? deviceLabel(d, t("userDrawer.device")) : t("userDrawer.sharedPlace");
}

/** What the device's own name falls back to: the placeholder of the rename field. */
function reported(d: BoundDevice): string {
  return d.hwid ? reportedName(d) || t("userDrawer.device") : t("userDrawer.sharedPlace");
}

function BoundDevices({ u }: { u: User }) {
  const settings = useSettings();
  const bound = useBoundDevices(u.id);
  const unbind = useUserMutation(userActions.unbindDevice);
  const ban = useUserMutation(userActions.banDevice);
  const rename = useUserMutation(userActions.renameDevice);
  const toast = useToast();
  const [pick, setPick] = useState<{ d: BoundDevice; act: "unbind" | "ban" | "rename" } | null>(null);
  const list = bound.data ?? [];
  const close = () => {
    rename.reset();
    setPick(null);
  };
  const fail = (e: unknown) => toast.error(errorText(e));
  // A refused name stands under the field; anything else is a toast.
  const renameError = rename.error instanceof ApiError ? rename.error.fields.name : undefined;
  if (!settings.data?.device_binding && list.length === 0) return <DeviceBans u={u} />;
  return (
    <>
      <h4 className="mb-2 flex justify-between gap-2 text-xs font-medium text-[var(--ink-500)]">
        {t("userDrawer.boundTitle")}
        {bound.data ? <span className="num">{u.device_limit != null ? t("userDrawer.boundCount", { n: list.length, limit: u.device_limit }) : list.length}</span> : null}
      </h4>
      {bound.data === undefined && !bound.isError ? (
        <Skeleton style={{ height: 52, borderRadius: 16 }} />
      ) : bound.data === undefined ? (
        <ErrorState text={errorText(bound.error)} onRetry={() => void bound.refetch()} />
      ) : list.length === 0 ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.boundEmpty")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {list.map((d) => {
            const name = deviceName(d);
            const meta = deviceDetails(d, !!d.hwid);
            return (
              <li key={d.id} className="panel-soft grid grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 p-2">
                <span className="grid h-9 w-9 place-items-center rounded-[10px] bg-[var(--hover)] text-[var(--ink-600)]" aria-hidden>
                  {!d.hwid ? <Layers size={18} /> : desktopOS.test(d.os) ? <Laptop size={18} /> : <Smartphone size={18} />}
                </span>
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium" title={name}>
                    {name}
                  </div>
                  <div className="truncate text-xs text-[var(--ink-500)]">
                    {meta ? `${meta} · ` : ""}
                    {d.online ? <span className="text-[var(--leaf-700)]">{t("users.onlineNow")}</span> : ago(d.last_seen)}
                  </div>
                  {d.traffic_up + d.traffic_down > 0 ? (
                    <div className="num text-xs text-[var(--ink-500)]" title={t("userDrawer.deviceTrafficHint")}>
                      {t("userDrawer.deviceTraffic", { down: bytes(d.traffic_down), up: bytes(d.traffic_up) })}
                    </div>
                  ) : null}
                </div>
                <div className="flex items-center gap-1">
                  <button type="button" className="icon-btn" aria-label={t("userDrawer.renameDeviceLabel", { name })} title={t("userDrawer.renameDevice")} onClick={() => setPick({ d, act: "rename" })}>
                    <Pencil size={16} aria-hidden />
                  </button>
                  {/* Only a device with its own id: the shared place is anyone's app without one. */}
                  {d.hwid ? (
                    <button type="button" className="icon-btn" aria-label={t("userDrawer.banLabel", { name })} title={t("userDrawer.ban")} onClick={() => setPick({ d, act: "ban" })}>
                      <Ban size={16} aria-hidden />
                    </button>
                  ) : null}
                  <button type="button" className="icon-btn" aria-label={t("userDrawer.unbindLabel", { name })} title={t("userDrawer.unbind")} onClick={() => setPick({ d, act: "unbind" })}>
                    <Unlink size={16} aria-hidden />
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      <p className="mt-2 text-xs text-[var(--ink-500)]">{settings.data?.device_require_hwid ? t("userDrawer.boundNoteStrict") : t("userDrawer.boundNote")}</p>
      <DeviceBans u={u} />
      <Confirm
        open={pick?.act === "unbind"}
        onOpenChange={(v) => !v && close()}
        title={t("userDrawer.unbindTitle", { name: pick ? deviceName(pick.d) : "" })}
        text={pick && !pick.d.hwid ? t("userDrawer.unbindSharedText") : t("userDrawer.unbindText")}
        confirm={t("userDrawer.unbind")}
        danger
        loading={unbind.isPending}
        onConfirm={() =>
          pick &&
          unbind.mutate(
            { id: u.id, device: pick.d.id },
            {
              onSuccess: () => {
                toast.ok(t("userDrawer.unbound", { name: deviceName(pick.d) }));
                close();
              },
              onError: fail,
            },
          )
        }
      />
      <Confirm
        open={pick?.act === "ban"}
        onOpenChange={(v) => !v && close()}
        title={t("userDrawer.banTitle", { name: pick ? deviceName(pick.d) : "" })}
        text={t("userDrawer.banText")}
        confirm={t("userDrawer.ban")}
        danger
        loading={ban.isPending}
        onConfirm={() =>
          pick &&
          ban.mutate(
            { id: u.id, device: pick.d.id },
            {
              onSuccess: () => {
                toast.ok(t("userDrawer.banned", { name: deviceName(pick.d) }));
                close();
              },
              onError: fail,
            },
          )
        }
      />
      <NameDialog
        open={pick?.act === "rename"}
        onOpenChange={(v) => !v && close()}
        title={t("userDrawer.renameDeviceTitle")}
        label={t("userDrawer.renameDeviceField")}
        hint={t("userDrawer.renameDeviceHint")}
        initial={pick?.d.name ?? ""}
        placeholder={pick ? reported(pick.d) : undefined}
        maxLength={DEVICE_NAME_MAX}
        optional
        error={renameError}
        loading={rename.isPending}
        onSubmit={(name) =>
          pick &&
          rename.mutate(
            { id: u.id, device: pick.d.id, name },
            {
              onSuccess: (r) => {
                toast.ok(r.name ? t("userDrawer.deviceRenamed", { name: r.name }) : t("userDrawer.deviceNameCleared"));
                close();
              },
              onError: (e) => {
                if (!(e instanceof ApiError && e.fields.name)) fail(e);
              },
            },
          )
        }
      />
    </>
  );
}

/**
 * Devices banned from the subscription: they get no keys until the admin lets them back.
 * A device the subscriber unbound is here too while it may not come back (until): the
 * admin may let it in earlier.
 */
function DeviceBans({ u }: { u: User }) {
  const bans = useDeviceBans(u.id);
  const unban = useUserMutation(userActions.unbanDevice);
  const toast = useToast();
  const [pick, setPick] = useState<DeviceBan | null>(null);
  const label = (b: DeviceBan) => b.label || t("userDrawer.device");
  const list = bans.data ?? [];
  // Nothing to show when nothing is banned: most clients never have a ban.
  if (bans.isSuccess && list.length === 0) return null;
  return (
    <>
      <h4 className="mt-5 mb-2 flex justify-between gap-2 text-xs font-medium text-[var(--ink-500)]">
        {t("userDrawer.bansTitle")}
        {bans.data ? <span className="num">{list.length}</span> : null}
      </h4>
      {bans.isPending ? (
        <Skeleton style={{ height: 52, borderRadius: 16 }} />
      ) : bans.isError ? (
        <ErrorState text={errorText(bans.error)} onRetry={() => void bans.refetch()} />
      ) : (
        <ul className="flex flex-col gap-2" aria-label={t("userDrawer.bansTitle")}>
          {list.map((b) => (
            <li key={b.id} className="panel-soft grid grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 p-2">
              <span className={`grid h-9 w-9 place-items-center rounded-[10px] bg-[var(--hover)] ${b.until ? "text-[var(--mikan-600)]" : "text-[var(--berry-600)]"}`} aria-hidden>
                {b.until ? <Hourglass size={18} /> : <Ban size={18} />}
              </span>
              <div className="min-w-0">
                <div className="truncate text-[13px] font-medium" title={label(b)}>
                  {label(b)}
                </div>
                <div className="truncate text-xs text-[var(--ink-500)]">
                  {b.until
                    ? t("userDrawer.pausedUntil", { when: dateShort(b.until), time: time(b.until) })
                    : b.admin
                      ? t("userDrawer.bannedBy", { when: dateShort(b.banned_at), admin: b.admin })
                      : t("userDrawer.bannedAt", { when: dateShort(b.banned_at) })}
                </div>
              </div>
              <Button size="sm" variant="ghost" onClick={() => setPick(b)} aria-label={t("userDrawer.unbanLabel", { name: label(b) })}>
                {t("userDrawer.unban")}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Confirm
        open={pick !== null}
        onOpenChange={(v) => !v && setPick(null)}
        title={t(pick?.until ? "userDrawer.unpauseTitle" : "userDrawer.unbanTitle", { name: pick ? label(pick) : "" })}
        text={t("userDrawer.unbanText")}
        confirm={t("userDrawer.unban")}
        loading={unban.isPending}
        onConfirm={() =>
          pick &&
          unban.mutate(
            { id: u.id, ban: pick.id },
            {
              onSuccess: () => {
                toast.ok(t("userDrawer.unbanned", { name: label(pick) }));
                setPick(null);
              },
              onError: (e) => toast.error(errorText(e)),
            },
          )
        }
      />
    </>
  );
}
