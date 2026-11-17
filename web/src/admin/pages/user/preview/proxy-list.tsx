import { useNodes } from "../../../../api/hooks";
import { t } from "../../../../i18n";
import { nodeLabel } from "../../../../lib/node-label";
import type { Preview } from "./model";

// A server's address with its port; an IPv6 address in brackets, as a link writes it.
const address = (server: string, port: string) => `${server.includes(":") ? `[${server}]` : server}:${port}`;

/** The servers of the answer in the order the app lists them: the name, the protocol, the address and the node. */
export function ProxyList({ proxies }: { proxies: Preview["proxies"] }) {
  const nodes = useNodes();
  if (proxies.length === 0) return <p className="text-xs text-[var(--ink-500)]">{t("userDrawer.preview.proxiesNone")}</p>;
  return (
    <ol className="flex flex-col gap-2">
      {proxies.map((x, i) => {
        const node = nodes.data?.find((n) => n.id === x.node_id);
        return (
          <li key={`${x.name}-${i}`} className="flex min-w-0 flex-col">
            <span className="truncate text-[13px] font-medium" title={x.name}>
              {x.name}
            </span>
            <span className="mono truncate text-xs text-[var(--ink-500)]">{[x.type, address(x.server, x.ports || String(x.port)), node ? nodeLabel(node) : ""].filter(Boolean).join(" · ")}</span>
          </li>
        );
      })}
    </ol>
  );
}
