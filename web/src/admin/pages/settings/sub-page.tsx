// Settings → Subscription page: how the page and the Mini App look, what they show and in
// what order, with the page itself next to the form. One draft for the page's settings,
// saved together; images and instructions are saved as they are made. With a group (#290)
// the editor edits the group's own page: its clients see it in place of the panel's, while
// the brand, its colour, the images and the instructions stay the panel's.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useBlocker } from "@tanstack/react-router";
import { RotateCcw } from "lucide-react";
import { useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { groupActions, qk, useGroupPage, useGroups } from "../../../api/hooks";
import { SaveBar, WithPreview } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Button, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import type { Platform } from "../../../sub/apps";
import type { SETTINGS_PARTS } from "../../search";
import { GroupBadge } from "../groups/badge";
import { DocsSection } from "./sub-docs";
import { AppsSection } from "./sub-page/apps";
import { BlocksSection } from "./sub-page/blocks";
import { BrandSection } from "./sub-page/brand";
import { CSSSection, LookSection } from "./sub-page/look";
import { COLOR, type Form, type Set, type View } from "./sub-page/parts";
import { Preview } from "./sub-page/preview";

/** The editor's parts, listed under "Subscription page" in the settings' section list. */
export type SubPagePart = (typeof SETTINGS_PARTS.page)[number];

type Section = SubPagePart;

/** The group whose own page the editor edits, as the editor tells it. */
type PageGroup = { id: number; name: string; own: boolean };

function useSubPage() {
  return useQuery({ queryKey: qk.subPage, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/sub-page", { signal })) });
}

/** The editor of the subscription page; the open part is kept by the settings page (in the URL), and so is the group whose page it is. */
export function SubPageSettings({ section, onSection, group }: { section: SubPagePart; onSection: (s: SubPagePart) => void; group?: number }) {
  if (group) return <GroupPageSettings group={group} section={section} onSection={onSection} />;
  return <PanelPageSettings section={section} onSection={onSection} />;
}

function PanelPageSettings({ section, onSection }: { section: SubPagePart; onSection: (s: SubPagePart) => void }) {
  const page = useSubPage();
  return (
    <QueryBoundary query={page} pending={<Skeleton style={{ height: 520, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) => <Editor v={v} section={section} setSection={onSection} />}
    </QueryBoundary>
  );
}

function GroupPageSettings({ group, section, onSection }: { group: number; section: SubPagePart; onSection: (s: SubPagePart) => void }) {
  const page = useGroupPage(group);
  const groups = useGroups();
  const g = groups.data?.find((x) => x.id === group);
  if (groups.data && !g) {
    // A group deleted meanwhile, or a link to one there never was.
    return (
      <section className="card glass">
        <p className="text-[13px] text-[var(--ink-600)]">{t("settings.page.groupGone")}</p>
        <Link to="/settings" search={{ tab: "page", part: section }} className="btn btn-glass btn-sm mt-3">
          {t("settings.page.groupBack")}
        </Link>
      </section>
    );
  }
  return (
    <QueryBoundary query={page} pending={<Skeleton style={{ height: 520, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) => (
        <>
          <div className="banner info mb-3 flex flex-wrap items-center justify-between gap-3" role="status">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                {g ? <GroupBadge group={g} /> : null}
                <span className="text-[13px] font-medium">{v.own ? t("settings.page.groupOwn") : t("settings.page.groupNotOwn")}</span>
              </div>
              <p className="mt-1 text-xs">{t("settings.page.groupShared")}</p>
            </div>
            <Link to="/settings" search={{ tab: "page", part: section }} className="btn btn-glass btn-sm">
              {t("settings.page.groupBack")}
            </Link>
          </div>
          <Editor key={group} v={v} section={section} setSection={onSection} group={{ id: group, name: g?.name ?? "", own: v.own }} />
        </>
      )}
    </QueryBoundary>
  );
}

function Editor({ v, section, setSection, group }: { v: View; section: Section; setSection: (s: Section) => void; group?: PageGroup }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { draft, setDraft, dirty, reset } = useDraft<Form>({ config: v.config, brand_accent: v.brand_accent });
  // The platform the apps section edits: the preview opens the same tab.
  const [appsTab, setAppsTab] = useState<Platform>("ios");
  const [live, setLive] = useState("");
  const [resetting, setResetting] = useState(false);
  const set: Set = (f) => setDraft((d) => ({ ...d, config: f(d.config) }));
  const save = useMutation({
    mutationFn: (f: Form): Promise<View> => {
      // A group's page is its document alone: the brand's colour is the panel's.
      if (group) return groupActions.putPage({ id: group.id, config: f.config });
      const body: Schemas["PutSubPageInputBody"] = { config: f.config };
      if (f.brand_accent !== v.brand_accent) body.brand_accent = f.brand_accent.trim().toUpperCase();
      return unwrap(api.PUT("/api/v1/sub-page", { body }));
    },
    onSuccess: (data) => {
      if (group) {
        qc.setQueryData(qk.groupPage(group.id), data);
        void qc.invalidateQueries({ queryKey: qk.groups });
      } else {
        qc.setQueryData(qk.subPage, data);
        void qc.invalidateQueries({ queryKey: qk.settings });
      }
      // The draft as the server keeps it (the colour in capitals): else the bar and the
      // question on leaving stayed after the save.
      setDraft({ config: data.config, brand_accent: data.brand_accent });
      toast.ok(group ? t("settings.page.groupSaved", { name: group.name }) : t("settings.page.saved"));
    },
    onError: (e) => {
      toast.error(errorText(e));
      // The first field the server refused: its section opens.
      const field = e instanceof ApiError ? Object.keys(e.fields)[0] : undefined;
      if (field) setSection(sectionOf(field));
    },
  });
  const errors = fieldErrors(save.error);
  const accentMissing = draft.config.look.accent === "brand" && !COLOR.test(draft.brand_accent.trim());
  const submit = () => {
    if (accentMissing) {
      setSection("look");
      toast.error(t("errors.api.color_required"));
      return;
    }
    save.mutate(draft);
  };
  // Leaving the page, this section or this page's group drops the draft: ask first.
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => {
      const to = next.search as { tab?: string; group?: number };
      return dirty && (current.pathname !== next.pathname || to.tab !== "page" || to.group !== group?.id);
    },
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });

  return (
    <>
      <WithPreview title={t("settings.page.preview")} preview={(bare) => <Preview v={v} draft={draft} platform={section === "apps" ? appsTab : undefined} bare={bare} />}>
            {section === "look" ? (
              <LookSection draft={draft} setDraft={setDraft} set={set} errors={errors} accentMissing={accentMissing} background={v.background} accentShared={!!group} />
            ) : section === "brand" ? (
              <BrandSection draft={draft} setDraft={setDraft} set={set} errors={errors} logo={v.logo} brandName={v.brand} />
            ) : section === "blocks" ? (
              <BlocksSection config={draft.config} set={set} errors={errors} announce={setLive} />
            ) : section === "apps" ? (
              <AppsSection config={draft.config} set={set} announce={setLive} platform={appsTab} setPlatform={setAppsTab} />
            ) : section === "docs" ? (
              <DocsSection announce={setLive} />
            ) : (
              <CSSSection config={draft.config} set={set} error={errors["config.css"]} />
            )}
          <div className="flex flex-wrap items-center gap-2 px-1">
            <Button variant="ghost" size="sm" onClick={() => setResetting(true)}>
              <RotateCcw size={16} aria-hidden /> {t("settings.page.reset")}
            </Button>
            <span className="text-xs text-[var(--ink-500)]">{t("settings.page.resetHint")}</span>
          </div>
      </WithPreview>
      <p className="sr-only" role="status" aria-live="polite">
        {live}
      </p>
      {/* A group that shows the panel's page gets its own with the first save, dirty or not. */}
      <SaveBar dirty={dirty || (!!group && !group.own)} saving={save.isPending} onSave={submit} onReset={reset} label={group && !group.own ? t("settings.page.groupCreate") : undefined} />
      <Confirm
        open={resetting}
        onOpenChange={setResetting}
        title={t("settings.page.resetTitle")}
        text={t("settings.page.resetText")}
        confirm={t("settings.page.resetConfirm")}
        danger
        onConfirm={() => {
          setDraft((d) => ({ ...d, config: structuredClone(v.defaults) }));
          setResetting(false);
        }}
      />
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("settings.page.leaveTitle")}
        text={t("settings.page.leaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
    </>
  );
}

/** The section of the editor a field the API refused is in. */
function sectionOf(field: string): Section {
  if (field.startsWith("config.blocks")) return "blocks";
  if (field.startsWith("config.apps")) return "apps";
  if (field.startsWith("config.css")) return "css";
  if (field.startsWith("config.look") || field === "brand_accent") return "look";
  return "brand";
}
