import * as Menu from "@radix-ui/react-dropdown-menu";
import { CalendarPlus, MoreHorizontal, Pencil, Power, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { useState } from "react";
import { ApiError, errorText, type User } from "../../api/client";
import { onePeriod, userActions, useUser, useUserMutation } from "../../api/hooks";
import { Disclosure } from "../../components/layout";
import { Confirm, Drawer, NameDialog } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { QueryBoundary } from "../../components/query";
import { Avatar, Button, Skeleton, StatePill } from "../../components/ui";
import { t } from "../../i18n";
import { ago, dateShort } from "../../lib/format";
import { GrantsSection } from "./user/grants";
import { PoolsSection, TariffSection, TrafficSection } from "./user/traffic";
import { ExpirySection } from "./user/expiry";
import { NoteSection, ProtocolsSection, SubscriptionSection, TelegramSection } from "./user/access";
import { DevicesSection } from "./user/devices";
import { OrganiseSection } from "./user/organise";
import { TorrentSection } from "./user/torrent";

export function UserDrawer({ id, onClose }: { id?: number; onClose: () => void }) {
  const user = useUser(id);
  const u = user.data;
  const [renaming, setRenaming] = useState(false);
  return (
    <Drawer
      wide
      open={!!id}
      onOpenChange={(v) => !v && onClose()}
      title={u?.name ?? t("userDrawer.fallbackTitle")}
      titleAction={
        u ? (
          <button type="button" className="icon-btn shrink-0" aria-label={t("userDrawer.renameLabel", { name: u.name })} title={t("userDrawer.rename")} onClick={() => setRenaming(true)}>
            <Pencil size={16} aria-hidden />
          </button>
        ) : undefined
      }
      lead={u ? <Avatar name={u.name} seed={u.id} size="lg" /> : undefined}
      meta={
        u ? (
          <>
            <StatePill state={u.state} />
            <span>{t("userDrawer.created", { date: dateShort(u.created_at) })}</span>
            <span className="mono">#{u.id}</span>
            {u.hidden ? <span>{t("users.hiddenBadge")}</span> : null}
            {u.online ? <span className="text-[var(--leaf-700)]">{t("userDrawer.online")}</span> : u.online_at ? <span>{t("userDrawer.seen", { ago: ago(u.online_at) })}</span> : null}
          </>
        ) : undefined
      }
    >
      <QueryBoundary
        query={user}
        pending={
          <div className="space-y-4 pt-5">
            <Skeleton style={{ height: 40 }} />
            <Skeleton style={{ height: 120, borderRadius: 16 }} />
            <Skeleton style={{ height: 160, borderRadius: 16 }} />
          </div>
        }
      >
        {/* A failed poll keeps the card (and the forms in it); the notice sits above it. */}
        {(data) => <UserBody key={data.id} u={data} onDeleted={onClose} onRename={() => setRenaming(true)} />}
      </QueryBoundary>
      {u ? <RenameUser u={u} open={renaming} onOpenChange={setRenaming} /> : null}
    </Drawer>
  );
}

/** A new name for the client: the link, the keys and the devices stay as they are. */
function RenameUser({ u, open, onOpenChange }: { u: User; open: boolean; onOpenChange: (v: boolean) => void }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  // A refused name stands under the field; anything else is a toast.
  const fieldError = update.error instanceof ApiError ? update.error.fields.name : undefined;
  return (
    <NameDialog
      open={open}
      onOpenChange={(v) => {
        if (!v) update.reset();
        onOpenChange(v);
      }}
      title={t("userDrawer.renameTitle")}
      label={t("userDrawer.renameField")}
      hint={t("userDrawer.renameHint")}
      initial={u.name}
      maxLength={100}
      error={fieldError}
      loading={update.isPending}
      onSubmit={(name) =>
        update.mutate(
          { id: u.id, body: { name } },
          {
            onSuccess: (r) => {
              onOpenChange(false);
              update.reset();
              toast.ok(t("userDrawer.renamed", { name: r.name }));
            },
            onError: (e) => {
              if (!(e instanceof ApiError && e.fields.name)) toast.error(errorText(e));
            },
          },
        )
      }
    />
  );
}

function UserBody({ u, onDeleted, onRename }: { u: User; onDeleted: () => void; onRename: () => void }) {
  const toast = useToast();
  const extend = useUserMutation(userActions.extend);
  const reset = useUserMutation(userActions.reset);
  const update = useUserMutation(userActions.update);
  const reissue = useUserMutation(userActions.reissue);
  const remove = useUserMutation(userActions.remove);
  const [confirm, setConfirm] = useState<"reissue" | "delete" | null>(null);
  const fail = (e: unknown) => toast.error(errorText(e));
  const disabled = u.state === "disabled";

  return (
    <>
      <div className="flex flex-wrap gap-2 py-4">
        <Button variant="primary" loading={extend.isPending} onClick={() => extend.mutate({ id: u.id, ...onePeriod(u) }, { onSuccess: (r) => toast.ok(t("userDrawer.extendedUntil", { date: dateShort(r.expires_at!) })), onError: fail })}>
          <CalendarPlus size={18} aria-hidden /> {u.billing_day != null ? t("userDrawer.extendMonth") : t("userDrawer.extend30")}
        </Button>
        <Button loading={reset.isPending} onClick={() => reset.mutate(u.id, { onSuccess: () => toast.ok(t("userDrawer.trafficReset")), onError: fail })}>
          <RotateCcw size={18} aria-hidden /> {t("users.resetTraffic")}
        </Button>
        <Button
          loading={update.isPending && update.variables?.body.disabled !== undefined}
          onClick={() =>
            update.mutate(
              { id: u.id, body: { disabled: !disabled } },
              { onSuccess: () => toast.ok(disabled ? t("userDrawer.enabledToast", { name: u.name }) : t("userDrawer.disabledToast", { name: u.name })), onError: fail },
            )
          }
        >
          <Power size={18} aria-hidden /> {disabled ? t("common.enable") : t("users.disable")}
        </Button>
        <Menu.Root>
          <Menu.Trigger asChild>
            <button type="button" className="icon-btn h-10 w-10" aria-label={t("userDrawer.more")}>
              <MoreHorizontal size={18} />
            </button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
              <Menu.Item className="menu-item" onSelect={onRename}>
                <Pencil size={16} aria-hidden /> {t("userDrawer.rename")}
              </Menu.Item>
              <Menu.Item className="menu-item" onSelect={() => setConfirm("reissue")}>
                <RefreshCw size={16} aria-hidden /> {t("userDrawer.reissue")}
              </Menu.Item>
              <Menu.Separator className="menu-sep" />
              <Menu.Item className="menu-item danger" onSelect={() => setConfirm("delete")}>
                <Trash2 size={16} aria-hidden /> {t("userDrawer.delete")}
              </Menu.Item>
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>

      {/* What a card is opened for comes first: the link to send, the term, the traffic. */}
      <SubscriptionSection u={u} onReissue={() => setConfirm("reissue")} />
      <ExpirySection u={u} />
      <TrafficSection u={u} />
      <DevicesSection u={u} />
      <TariffSection u={u} />
      <NoteSection u={u} />
      <Disclosure title={t("userDrawer.advanced")} sub={t("userDrawer.advancedSub")}>
        <PoolsSection u={u} />
        <GrantsSection u={u} />
        <ProtocolsSection u={u} />
        <TorrentSection u={u} />
        <TelegramSection u={u} />
        <OrganiseSection u={u} />
      </Disclosure>

      <Confirm
        open={confirm === "reissue"}
        onOpenChange={(v) => !v && setConfirm(null)}
        title={t("userDrawer.reissueTitle")}
        text={u.legacy ? `${t("userDrawer.reissueText")} ${t("userDrawer.legacyReissue")}` : t("userDrawer.reissueText")}
        confirm={t("userDrawer.reissueConfirm")}
        loading={reissue.isPending}
        onConfirm={() =>
          reissue.mutate(u.id, {
            onSuccess: () => {
              setConfirm(null);
              toast.ok(t("userDrawer.reissued"));
            },
            onError: fail,
          })
        }
      />
      <Confirm
        open={confirm === "delete"}
        onOpenChange={(v) => !v && setConfirm(null)}
        title={t("userDrawer.deleteTitle", { name: u.name })}
        text={t("userDrawer.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          remove.mutate(u.id, {
            onSuccess: () => {
              setConfirm(null);
              toast.ok(t("userDrawer.deleted", { name: u.name }));
              onDeleted();
            },
            onError: fail,
          })
        }
      />
    </>
  );
}
