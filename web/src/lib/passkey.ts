// Passkeys (WebAuthn): the panel sends its options as JSON ({publicKey: …}, binary fields in
// base64url) and takes the browser's answer as PublicKeyCredential.toJSON() gives it.

/** The browser can make and use passkeys, with the JSON forms of WebAuthn level 3. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && typeof window.PublicKeyCredential === "function" && typeof PublicKeyCredential.parseRequestOptionsFromJSON === "function";
}

type Options<T> = { publicKey: T };

/** Makes a passkey with the options of POST /auth/passkeys/begin. */
export async function createPasskey(options: unknown): Promise<PublicKeyCredentialJSON> {
  const o = options as Options<PublicKeyCredentialCreationOptionsJSON>;
  const cred = (await navigator.credentials.create({ publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(o.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException("no credential", "NotAllowedError");
  return cred.toJSON();
}

/** Signs with a passkey for the options of POST /auth/login/passkey/begin. */
export async function getPasskey(options: unknown): Promise<PublicKeyCredentialJSON> {
  const o = options as Options<PublicKeyCredentialRequestOptionsJSON>;
  const cred = (await navigator.credentials.get({ publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(o.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException("no credential", "NotAllowedError");
  return cred.toJSON();
}

/** The admin closed the browser's prompt, or it timed out: nothing to say about it. */
export function passkeyCancelled(e: unknown): boolean {
  return e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError");
}
