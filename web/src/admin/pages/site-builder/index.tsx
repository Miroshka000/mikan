import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronLeft } from "lucide-react";
import { api, errorText, unwrap } from "../../../api/client";
import { useSupportMode } from "../../../api/support";
import { ErrorState, PageHeader, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ClosedForSupport } from "../../support-closed";
import { Editor } from "./editor";
import { sitePageKey } from "./save";
import "./site-builder.css";

/**
 * The builder of the installation's own front page: a site of a few pages the panel and its
 * nodes show a stranger. The page loads what was saved and hands it to the editor.
 */
export function SiteBuilderPage() {
  const operator = !!useSupportMode();
  const saved = useQuery({ queryKey: sitePageKey, queryFn: () => unwrap(api.GET("/api/v1/settings/site-page")), enabled: !operator, staleTime: Infinity });
  if (saved.isSuccess && !operator) return <Editor key={saved.dataUpdatedAt} saved={saved.data} />;
  return (
    <>
      <Link to="/settings" search={{ tab: "security" }} className="mb-2 inline-flex items-center gap-1 rounded-lg text-[13px] font-medium text-[var(--ink-500)] hover:text-[var(--ink-900)]">
        <ChevronLeft size={16} aria-hidden /> {t("siteBuilder.back")}
      </Link>
      <PageHeader title={t("siteBuilder.title")} sub={t("siteBuilder.sub")} />
      {operator ? <ClosedForSupport /> : saved.isError ? <ErrorState text={errorText(saved.error)} onRetry={() => void saved.refetch()} /> : <Skeleton style={{ height: 560, borderRadius: 20 }} />}
    </>
  );
}
