import { Link } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { useState } from "react";
import { errorText } from "../../../../api/client";
import { groupActions, useGroupMutation, useGroupPage } from "../../../../api/hooks";
import { Confirm } from "../../../../components/overlay";
import { useToast } from "../../../../components/toast";
import { Button } from "../../../../components/ui";
import { t } from "../../../../i18n";
import type { Group } from "../model";

/**
 * The subscription page the group's clients see: the panel's, or the group's own, edited in
 * the page's editor (Settings → Subscription page) for the group. An own page starts as a copy
 * of the panel's; images, instructions and the brand stay the panel's. Each step saves at once.
 */
export function PageCard({ group }: { group: Group }) {
  const page = useGroupPage(group.id);
  const toast = useToast();
  const put = useGroupMutation(groupActions.putPage);
  const remove = useGroupMutation(groupActions.removePage);
  const [resetting, setResetting] = useState(false);
  const own = group.appearance.page;
  return (
    <div className="panel-soft flex flex-wrap items-center justify-between gap-3 p-4">
      {/* The words keep a readable width: the buttons go under them when both do not fit. */}
      <div className="min-w-[220px] flex-1">
        <div className="text-[13px] font-medium">{own ? t("groups.appearance.pageOwn") : t("groups.appearance.pageAsAll")}</div>
        <div className="mt-1 text-xs text-[var(--ink-500)]">{own ? t("groups.appearance.pageOwnSub") : t("groups.appearance.pageAsAllSub")}</div>
      </div>
      <div className="flex flex-wrap gap-2">
        {own ? (
          <>
            <Link to="/settings" search={{ tab: "page", part: "look", group: group.id }} className="btn btn-glass btn-sm">
              <ExternalLink size={16} aria-hidden /> {t("groups.appearance.pageEdit")}
            </Link>
            <Button size="sm" variant="ghost" onClick={() => setResetting(true)}>
              {t("groups.appearance.pageReset")}
            </Button>
          </>
        ) : (
          <Button
            size="sm"
            loading={put.isPending}
            disabled={!page.data}
            onClick={() =>
              page.data &&
              put.mutate({ id: group.id, config: page.data.config }, { onSuccess: () => toast.ok(t("groups.appearance.pageCreated")), onError: (e) => toast.error(errorText(e)) })
            }
          >
            {t("groups.appearance.pageCreate")}
          </Button>
        )}
      </div>
      <Confirm
        open={resetting}
        onOpenChange={setResetting}
        title={t("groups.appearance.pageResetTitle")}
        text={t("groups.appearance.pageResetText", { name: group.name })}
        confirm={t("groups.appearance.pageReset")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          remove.mutate(group.id, {
            onSuccess: () => {
              setResetting(false);
              toast.ok(t("groups.appearance.pageResetDone"));
            },
            onError: (e) => {
              setResetting(false);
              toast.error(errorText(e));
            },
          })
        }
      />
    </div>
  );
}
