import type { ApiError } from "../../../api/client";
import { tMaybe } from "../../../i18n";

/** Why a change of an address waits: the certificate of the new name is not there yet. */
export type CertRefusal = { field: string; code: string; reason: string };

/** What certRefusal reads of an ApiError: the codes and the values of its fields. */
type Refused = Pick<ApiError, "codes" | "values">;

function refused(e: unknown): e is Refused {
  return typeof e === "object" && e !== null && typeof (e as Refused).codes === "object" && typeof (e as Refused).values === "object";
}

/** The refusal of a save that waits for a new name's certificate, for the first of fields that has one. */
export function certRefusal(error: unknown, fields: string[]): CertRefusal | null {
  if (!refused(error)) return null;
  for (const field of fields) {
    const code = error.codes[field];
    if (code === "cert_not_ready" || code === "cert_ordering") {
      const acme = typeof error.values[field] === "string" ? (error.values[field] as string) : "";
      return { field, code, reason: (acme && tMaybe(`errors.acme.${acme}`)) || acme };
    }
  }
  return null;
}
