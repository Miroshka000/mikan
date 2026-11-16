// The groups of clients (#290): what each group sees. A client is in its own group or else its
// tariff's; a group's servers stand in for the tariff's, and the addresses for clients are
// shown or kept by group at each address.
import { useState } from "react";
import { ApiError, errorText } from "../../../api/client";
import { groupActions, useGroupMutation, useGroups } from "../../../api/hooks";
import { Confirm, Drawer } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { chipOf } from "../user-folders";
import { GroupForm, type GroupDraft } from "./form";
import { GroupHosts } from "./hosts";
import { groupServersDraft, type Group } from "./model";
import { GroupRow } from "./row";

const EMPTY: GroupDraft = { name: "", emoji: "", color: "gray", servers: groupServersDraft() };

/** The API's refusals of a form by field: the name taken, the servers chosen. */
function fieldErrors(e: unknown): Record<string, string | undefined> {
  return e instanceof ApiError ? e.fields : {};
}

/** A refusal the form shows by its fields; anything else is a toast. */
const byField = (e: unknown) => Object.keys(fieldErrors(e)).length > 0;

export function GroupsDrawer({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const groups = useGroups();
  const toast = useToast();
  const create = useGroupMutation(groupActions.create);
  const update = useGroupMutation(groupActions.update);
  const remove = useGroupMutation(groupActions.remove);
  const order = useGroupMutation(groupActions.order);
  const [editing, setEditing] = useState<number | null>(null);
  const [fresh, setFresh] = useState(0);
  const [removing, setRemoving] = useState<Group | null>(null);
  const move = (list: Group[], idx: number, by: -1 | 1) => {
    const ids = list.map((g) => g.id);
    const to = idx + by;
    if (order.isPending || to < 0 || to >= ids.length) return;
    [ids[idx], ids[to]] = [ids[to]!, ids[idx]!];
    order.mutate(ids, { onError: (e) => toast.error(errorText(e)) });
  };
  const only = removing ? removing.hosts.filter((h) => h.mode === "only").length : 0;
  return (
    <Drawer wide open={open} onOpenChange={onOpenChange} title={t("groups.title")} meta={t("groups.meta")}>
      <div className="pt-5">
        <p className="text-[13px] text-[var(--ink-500)]">{t("groups.intro")}</p>
        <section className="panel-soft mt-4 p-4" aria-label={t("groups.new")}>
          <h3 className="mb-3 text-sm font-semibold">{t("groups.new")}</h3>
          <GroupForm
            // A saved group starts the form over; a refused one keeps what was typed.
            key={fresh}
            initial={EMPTY}
            busy={create.isPending}
            errors={fieldErrors(create.error)}
            submit={(body) =>
              create.mutate(body, {
                onSuccess: (g) => {
                  setFresh((n) => n + 1);
                  toast.ok(t("groups.created", { name: g.name }));
                },
                onError: (e) => {
                  if (!byField(e)) toast.error(errorText(e));
                },
              })
            }
          />
        </section>
        <QueryBoundary
          query={groups}
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
              <p className="mt-5 text-[13px] text-[var(--ink-500)]">{t("groups.none")}</p>
            ) : (
              <ul className="row-list mt-5" aria-label={t("groups.listLabel")}>
                {list.map((g, idx) => (
                  <li key={g.id} className="py-3">
                    {editing === g.id ? (
                      <div className="flex flex-col gap-4">
                        <GroupForm
                          initial={{ ...chipOf(g), servers: groupServersDraft(g) }}
                          busy={update.isPending}
                          errors={fieldErrors(update.error)}
                          onCancel={() => {
                            update.reset();
                            setEditing(null);
                          }}
                          submit={(body) =>
                            update.mutate(
                              { id: g.id, body },
                              {
                                onSuccess: () => {
                                  setEditing(null);
                                  toast.ok(t("groups.saved"));
                                },
                                onError: (e) => {
                                  if (!byField(e)) toast.error(errorText(e));
                                },
                              },
                            )
                          }
                        />
                        <section aria-label={t("groups.hostsLabel")}>
                          <h4 className="mb-1 text-[13px] font-semibold">{t("groups.hostsTitle")}</h4>
                          <p className="mb-2 text-xs text-[var(--ink-500)]">{t("groups.hostsHint")}</p>
                          <GroupHosts group={g} />
                        </section>
                      </div>
                    ) : (
                      <GroupRow
                        group={g}
                        first={idx === 0}
                        last={idx === list.length - 1}
                        ordering={order.isPending}
                        onMove={list.length > 1 ? (by) => move(list, idx, by) : undefined}
                        onEdit={() => {
                          update.reset();
                          setEditing(g.id);
                        }}
                        onDelete={() => setRemoving(g)}
                      />
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
        title={t("groups.deleteTitle", { name: removing?.name ?? "" })}
        text={only > 0 ? `${t("groups.deleteText", { n: removing?.users ?? 0 })} ${t("groups.deleteHosts", { n: only })}` : t("groups.deleteText", { n: removing?.users ?? 0 })}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          removing &&
          remove.mutate(removing.id, {
            onSuccess: () => {
              setRemoving(null);
              toast.ok(t("groups.deleted", { name: removing.name }));
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
