import type { Preview } from "./model";

/** The headers of the answer, a value the apps read as base64 told as text under it. */
export function HeaderList({ headers }: { headers: Preview["headers"] }) {
  return (
    <ul className="panel-soft flex flex-col gap-2 p-3">
      {headers.map((h, i) => (
        <li key={`${h.name}-${i}`} className="min-w-0 text-xs">
          <span className="mono font-medium">{h.name}:</span> <span className="mono break-all text-[var(--ink-700)]">{h.value}</span>
          {h.text ? <div className="mt-1 break-words whitespace-pre-wrap text-[var(--ink-500)]">{h.text}</div> : null}
        </li>
      ))}
    </ul>
  );
}
