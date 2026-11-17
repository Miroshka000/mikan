import { ArrowLeft } from "lucide-react";
import { useState } from "react";
import { Button, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { GroupAccess } from "./access";
import { AppearanceForm } from "./appearance/form";
import { GroupBadge } from "./badge";
import type { Group } from "./model";

type Tab = "access" | "appearance";

/**
 * One group, as the issue words it (#290): "Access", what its clients see, and "Appearance",
 * how their subscriptions look. Each tab saves on its own.
 */
export function GroupEditor({ group, onBack }: { group: Group; onBack: () => void }) {
  const [tab, setTab] = useState<Tab>("access");
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft size={16} aria-hidden /> {t("groups.back")}
        </Button>
        <GroupBadge group={group} />
      </div>
      <Segmented
        label={t("groups.tabs")}
        block
        value={tab}
        onChange={setTab}
        options={[
          { value: "access", label: t("groups.access") },
          { value: "appearance", label: t("groups.appearance.title") },
        ]}
      />
      {/* A tab's draft is the group's: another group, or the same one opened again, starts afresh. */}
      {tab === "access" ? <GroupAccess key={group.id} group={group} /> : <AppearanceForm key={group.id} group={group} />}
    </div>
  );
}
