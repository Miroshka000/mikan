// Folders of the users' list: groups the admin makes (family, friends, clients). A user is
// in one folder at most; a folder only sorts the list and never changes what a user may do.
import * as Menu from "@radix-ui/react-dropdown-menu";
import clsx from "clsx";
import { ArrowDown, ArrowUp, Check, Pencil, Trash2 } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { ApiError, errorText, type Schemas } from "../../api/client";
import { folderActions, useFolderMutation, useFolders } from "../../api/hooks";
import { Confirm, Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, Field, Skeleton } from "../../components/ui";
import { t } from "../../i18n";

export type Folder = Schemas["FolderView"];

/** The palette the API knows; the CSS gives each name its colour (styles/app.css, .fc-*). */
const FOLDER_COLORS = ["gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"] as const;
type FolderColor = (typeof FOLDER_COLORS)[number];

const colorOf = (c: string): FolderColor => ((FOLDER_COLORS as readonly string[]).includes(c) ? (c as FolderColor) : "gray");

/** The folder's emoji, or a dot in its colour. */
export function FolderMark({ folder }: { folder: Pick<Folder, "color" | "emoji"> }) {
  return folder.emoji ? (
    <span className="fmark" aria-hidden>
      {folder.emoji}
    </span>
  ) : (
    <i className={clsx("fdot", `fc-${colorOf(folder.color)}`)} aria-hidden />
  );
}

/** A small label with the folder's name, for a row of the list. */
export function FolderBadge({ folder }: { folder: Folder }) {
  return (
    <span className={clsx("fbadge", `fc-${colorOf(folder.color)}`)} title={t("folders.badge", { name: folder.name })}>
      <FolderMark folder={folder} />
      <span>{folder.name}</span>
    </span>
  );
}

/**
 * The items of a menu that puts a user (or several) into a folder; `current` is the folder
 * they are in now (null: none). Its caller says what picking one does.
 */
export function FolderMenuItems({ folders, current, onPick }: { folders: Folder[]; current?: number | null; onPick: (id: number | null) => void }) {
  return (
    <>
      {folders.map((f) => (
        <Menu.Item key={f.id} className="menu-item" disabled={current === f.id} onSelect={() => onPick(f.id)}>
          <FolderMark folder={f} />
          <span className="min-w-0 flex-1 truncate">{f.name}</span>
          {current === f.id ? <Check size={16} aria-hidden /> : null}
        </Menu.Item>
      ))}
      {folders.length > 0 ? <Menu.Separator className="menu-sep" /> : null}
      <Menu.Item className="menu-item" disabled={current === null} onSelect={() => onPick(null)}>
        <span className="flex-1">{t("users.noFolder")}</span>
        {current === null ? <Check size={16} aria-hidden /> : null}
      </Menu.Item>
    </>
  );
}

type Draft = { name: string; emoji: string; color: FolderColor };
const EMPTY: Draft = { name: "", emoji: "", color: "gray" };

function FolderForm({
  initial,
  submit,
  busy,
  error,
  onCancel,
}: {
  initial: Draft;
  submit: (d: Draft) => void;
  busy: boolean;
  error?: string;
  onCancel?: () => void;
}) {
  const uid = useId();
  const [draft, setDraft] = useState(initial);
  const ok = draft.name.trim() !== "";
  const send = (e: FormEvent) => {
    e.preventDefault();
    if (ok && !busy) submit({ ...draft, name: draft.name.trim(), emoji: draft.emoji.trim() });
  };
  return (
    <form onSubmit={send} noValidate className="flex flex-col gap-4">
      <div className="grid grid-cols-[minmax(0,1fr)_96px] gap-3">
        <Field label={t("folders.name")} htmlFor={`${uid}-name`} error={error}>
          <input
            id={`${uid}-name`}
            className="input"
            value={draft.name}
            maxLength={40}
            placeholder={t("folders.namePlaceholder")}
            aria-invalid={!!error}
            autoFocus
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
        </Field>
        <Field label={t("folders.emoji")} htmlFor={`${uid}-emoji`}>
          <input id={`${uid}-emoji`} className="input text-center" value={draft.emoji} maxLength={16} placeholder="🙂" onChange={(e) => setDraft({ ...draft, emoji: e.target.value })} />
        </Field>
      </div>
      <Field label={t("folders.color")}>
        <div role="radiogroup" aria-label={t("folders.color")} className="flex flex-wrap gap-2">
          {FOLDER_COLORS.map((c) => (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={draft.color === c}
              aria-label={t(`folders.colors.${c}`)}
              title={t(`folders.colors.${c}`)}
              className={clsx("swatch", `fc-${c}`)}
              onClick={() => setDraft({ ...draft, color: c })}
            >
              {draft.color === c ? <Check size={16} aria-hidden /> : null}
            </button>
          ))}
        </div>
      </Field>
      <div className="flex justify-end gap-2">
        {onCancel ? (
          <Button variant="ghost" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        ) : null}
        <Button variant="primary" type="submit" loading={busy} disabled={!ok}>
          {onCancel ? t("common.save") : t("folders.add")}
        </Button>
      </div>
    </form>
  );
}

/** The folders of the list: make, rename, recolour, put in order, delete. */
export function FoldersDrawer({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const folders = useFolders();
  const toast = useToast();
  const create = useFolderMutation(folderActions.create);
  const update = useFolderMutation(folderActions.update);
  const remove = useFolderMutation(folderActions.remove);
  const order = useFolderMutation(folderActions.order);
  const [editing, setEditing] = useState<number | null>(null);
  const [fresh, setFresh] = useState(0);
  const [removing, setRemoving] = useState<Folder | null>(null);
  // A form's own mistake (a name already taken) stands under its field; the rest is a toast.
  const nameError = (e: unknown) => (e instanceof ApiError && e.fields.name ? e.fields.name : undefined);
  const move = (list: Folder[], idx: number, by: -1 | 1) => {
    const ids = list.map((f) => f.id);
    const to = idx + by;
    if (order.isPending || to < 0 || to >= ids.length) return;
    [ids[idx], ids[to]] = [ids[to]!, ids[idx]!];
    order.mutate(ids, { onError: (e) => toast.error(errorText(e)) });
  };
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t("folders.title")} meta={t("folders.meta")}>
      <div className="pt-5">
        <p className="text-[13px] text-[var(--ink-500)]">{t("folders.intro")}</p>
        <section className="panel-soft mt-4 p-4" aria-label={t("folders.new")}>
          <h3 className="mb-3 text-sm font-semibold">{t("folders.new")}</h3>
          <FolderForm
            // A saved folder starts the form over; a refused one keeps what was typed.
            key={fresh}
            initial={EMPTY}
            busy={create.isPending}
            error={nameError(create.error)}
            submit={(d) =>
              create.mutate(d, {
                onSuccess: (f) => {
                  setFresh((n) => n + 1);
                  toast.ok(t("folders.created", { name: f.name }));
                },
                onError: (e) => {
                  if (!nameError(e)) toast.error(errorText(e));
                },
              })
            }
          />
        </section>
        <QueryBoundary
          query={folders}
          pending={
            <div className="mt-5 flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
              {[0, 1].map((i) => (
                <Skeleton key={i} style={{ height: 56 }} />
              ))}
            </div>
          }
        >
          {(list) =>
            list.length === 0 ? (
              <p className="mt-5 text-[13px] text-[var(--ink-500)]">{t("folders.none")}</p>
            ) : (
              <ul className="row-list mt-5" aria-label={t("folders.listLabel")}>
                {list.map((f, idx) => (
                  <li key={f.id} className="py-3">
                    {editing === f.id ? (
                      <FolderForm
                        initial={{ name: f.name, emoji: f.emoji, color: colorOf(f.color) }}
                        busy={update.isPending}
                        error={nameError(update.error)}
                        onCancel={() => {
                          update.reset();
                          setEditing(null);
                        }}
                        submit={(d) =>
                          update.mutate(
                            { id: f.id, body: d },
                            {
                              onSuccess: () => {
                                setEditing(null);
                                toast.ok(t("folders.saved"));
                              },
                              onError: (e) => {
                                if (!nameError(e)) toast.error(errorText(e));
                              },
                            },
                          )
                        }
                      />
                    ) : (
                      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                        <div className="flex min-w-0 items-center gap-3">
                          <FolderMark folder={f} />
                          <div className="min-w-0">
                            <div className="truncate text-[13px] font-semibold">{f.name}</div>
                            <div className="text-xs text-[var(--ink-500)]">{t("folders.users", { n: f.users })}</div>
                          </div>
                        </div>
                        <div className="flex items-center gap-1">
                          {list.length > 1 ? (
                            <>
                              <button type="button" className="icon-btn" disabled={order.isPending || idx === 0} aria-label={t("folders.moveUp", { name: f.name })} title={t("folders.moveUp", { name: f.name })} onClick={() => move(list, idx, -1)}>
                                <ArrowUp size={16} aria-hidden />
                              </button>
                              <button type="button" className="icon-btn" disabled={order.isPending || idx === list.length - 1} aria-label={t("folders.moveDown", { name: f.name })} title={t("folders.moveDown", { name: f.name })} onClick={() => move(list, idx, 1)}>
                                <ArrowDown size={16} aria-hidden />
                              </button>
                            </>
                          ) : null}
                          <button type="button" className="icon-btn" aria-label={t("folders.edit", { name: f.name })} title={t("folders.edit", { name: f.name })} onClick={() => setEditing(f.id)}>
                            <Pencil size={16} aria-hidden />
                          </button>
                          <button type="button" className="icon-btn" aria-label={t("folders.delete", { name: f.name })} title={t("folders.delete", { name: f.name })} onClick={() => setRemoving(f)}>
                            <Trash2 size={16} aria-hidden />
                          </button>
                        </div>
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            )
          }
        </QueryBoundary>
      </div>
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("folders.deleteTitle", { name: removing?.name ?? "" })}
        text={t("folders.deleteText", { n: removing?.users ?? 0 })}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          removing &&
          remove.mutate(removing.id, {
            onSuccess: () => {
              setRemoving(null);
              toast.ok(t("folders.deleted", { name: removing.name }));
            },
            onError: (e) => {
              setRemoving(null);
              toast.error(errorText(e));
            },
          })
        }
      />
    </Drawer>
  );
}
