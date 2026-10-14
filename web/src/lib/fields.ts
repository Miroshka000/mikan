import { ApiError, errorText } from "../api/client";

/**
 * The API's field errors of a failed mutation (`{ public_host: "…" }`), empty when it did
 * not fail or failed for a reason that is not about a field (a network error, a 500).
 * Replaces `save.error instanceof ApiError ? save.error.fields : {}` at every form.
 */
export function fieldErrors(error: unknown): Record<string, string> {
  return error instanceof ApiError ? error.fields : {};
}

/**
 * The onError of a form's mutation. A refusal that names fields is shown under them: `show`
 * takes them when the form keeps them in state (a form that reads them off the mutation with
 * fieldErrors passes none); anything else is a toast. Tells whether the error named fields.
 */
export function onFormError(e: unknown, toast: { error: (text: string) => void }, show?: (fields: Record<string, string>) => void): boolean {
  const fields = fieldErrors(e);
  if (Object.keys(fields).length === 0) {
    toast.error(errorText(e));
    return false;
  }
  show?.(fields);
  return true;
}
