import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, errorText, unwrap } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { t } from "../../../i18n";
import { bundleSize, SiteBundle } from "../../../lib/site-builder";
import { pruneImages } from "./images";
import type { EditorStore } from "./store";

export const sitePageKey = ["site-page"] as const;

export type SaveTarget = { everywhere: boolean; max: number; panelHas: boolean; nodes: { id: number; name: string; has: boolean }[] };

/** The site is larger than the panel keeps. */
export class TooLarge extends Error {
  constructor(
    readonly size: number,
    readonly max: number,
  ) {
    super("too large");
  }
}

/**
 * Saves the site on the canvas: packs its files, keeps them in the panel and, when asked,
 * makes the site the page of the panel and of every node. A node that refuses is named; the
 * rest stay set.
 */
export function useSaveSite(store: EditorStore) {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: async (target: SaveTarget) => {
      const editing = store.doc;
      const doc = pruneImages(editing);
      const { files, single } = await new SiteBundle(doc).build();
      const size = bundleSize(files);
      if (size > target.max) throw new TooLarge(size, target.max);
      await unwrap(api.PUT("/api/v1/settings/site-page", { body: { doc: JSON.stringify(doc), html: single, files } }));
      store.markSaved(editing);
      const failed: string[] = [];
      if (!target.everywhere) return failed;
      if (!target.panelHas) await unwrap(api.PATCH("/api/v1/settings", { body: { site_template: "custom" } }));
      for (const n of target.nodes) {
        if (n.has) continue;
        try {
          await unwrap(api.PATCH("/api/v1/nodes/{id}", { params: { path: { id: n.id } }, body: { front_page: "custom" } }));
        } catch {
          failed.push(n.name);
        }
      }
      return failed;
    },
    onSuccess: (failed, target) => {
      if (failed.length) toast.error(t("siteBuilder.failedNodes", { names: failed.join(", ") }));
      else toast.ok(target.everywhere ? t("siteBuilder.savedEverywhere") : t("siteBuilder.saved"));
      for (const key of [sitePageKey, qk.settings, qk.nodes]) void qc.invalidateQueries({ queryKey: key });
    },
    onError: (e) => {
      const kb = (n: number) => Math.ceil(n / 1024);
      toast.error(e instanceof TooLarge ? t("siteBuilder.tooLarge", { size: kb(e.size), max: kb(e.max) }) : errorText(e));
    },
  });
}
