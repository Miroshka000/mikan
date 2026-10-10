// The bot's texts as Telegram shows them. The panel converts the admin's Markdown to
// Telegram's HTML (internal/panel/tgbot/markdown.go); the preview asks the panel for that
// HTML, so it is the very message the bot sends, and builds it here as React nodes with
// only the tags the bot can send: a text cannot put other markup on the admin's page.
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { api, errorText, unwrap } from "../../api/client";
import { Skeleton } from "../../components/ui";
import { t } from "../../i18n";

/** text after the admin stopped typing for a moment: the preview does not ask per key. */
function useSettled<T>(value: T, ms = 300): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), ms);
    return () => window.clearTimeout(timer);
  }, [value, ms]);
  return settled;
}

/** The message the bot would send for text, with vars filled in. */
export function useTgPreview(text: string, vars?: Record<string, string>) {
  const settled = useSettled(text);
  return useQuery({
    queryKey: ["telegram", "preview", settled, vars ?? null],
    queryFn: ({ signal }) => unwrap(api.POST("/api/v1/telegram/preview", { body: { text: settled, vars }, signal })),
    enabled: settled.trim() !== "",
    placeholderData: keepPreviousData,
    staleTime: Infinity,
    retry: false,
  });
}

type Tag = "strong" | "em" | "u" | "s" | "code" | "pre" | "blockquote";
const TAGS: Record<string, Tag | undefined> = { b: "strong", strong: "strong", i: "em", em: "em", u: "u", s: "s", code: "code", pre: "pre", blockquote: "blockquote" };

function safeHref(href: string | null): string | undefined {
  return href && /^(https?|tg):\/\//i.test(href) ? href : undefined;
}

function nodes(parent: Node): ReactNode[] {
  return Array.from(parent.childNodes).map((n, i) => {
    if (n.nodeType === Node.TEXT_NODE) return <Fragment key={i}>{n.textContent}</Fragment>;
    if (!(n instanceof Element)) return null;
    const tag = n.tagName.toLowerCase();
    const inner = nodes(n);
    if (tag === "a") {
      const href = safeHref(n.getAttribute("href"));
      return href ? (
        <a key={i} href={href} target="_blank" rel="noreferrer noopener" className="text-[#2481cc] underline">
          {inner}
        </a>
      ) : (
        <Fragment key={i}>{inner}</Fragment>
      );
    }
    if (tag === "tg-spoiler") {
      return (
        <span key={i} className="tg-spoiler" title={t("telegram.md.spoiler")}>
          {inner}
        </span>
      );
    }
    const El = TAGS[tag];
    return El ? <El key={i}>{inner}</El> : <Fragment key={i}>{inner}</Fragment>;
  });
}

/** Telegram's HTML of the panel as the chat shows it. */
export function TgHtml({ html }: { html: string }) {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");
  return <>{nodes(doc.body)}</>;
}

/** A message bubble for text, as the bot will send it; nothing while text is empty. */
export function TgTextPreview({ text, vars, label }: { text: string; vars?: Record<string, string>; label: string }) {
  const q = useTgPreview(text, vars);
  if (!text.trim()) return null;
  return (
    <div className="mt-2" aria-live="polite">
      <div className="mb-1 text-xs text-[var(--ink-500)]">{label}</div>
      {q.isError ? (
        <p className="text-xs text-[var(--berry-600)]" role="alert">
          {errorText(q.error)}
        </p>
      ) : q.data === undefined ? (
        <Skeleton style={{ height: 40, borderRadius: 14 }} />
      ) : (
        <div className="tg-chat">
          <div className={q.isPlaceholderData ? "tg-bubble opacity-70" : "tg-bubble"}>
            <TgHtml html={q.data.html} />
          </div>
        </div>
      )}
    </div>
  );
}

/** What Markdown the bot's texts take, in one line under the fields. */
export function MarkdownHint() {
  const marks = [
    ["**", "telegram.md.bold"],
    ["*", "telegram.md.italic"],
    ["__", "telegram.md.underline"],
    ["~~", "telegram.md.strike"],
    ["||", "telegram.md.spoiler"],
    ["`", "telegram.md.code"],
  ] as const;
  return (
    <p className="text-xs leading-5 text-[var(--ink-500)]">
      {t("telegram.md.lead")}{" "}
      {marks.map(([m, key]) => (
        <Fragment key={key}>
          <code className="mono">
            {m}
            {t(key)}
            {m}
          </code>{" "}
        </Fragment>
      ))}
      <code className="mono">[{t("telegram.md.link")}](https://…)</code> <code className="mono">&gt; {t("telegram.md.quote")}</code>. {t("telegram.md.tail")}
    </p>
  );
}
