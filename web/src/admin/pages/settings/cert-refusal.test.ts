import { describe, expect, it } from "vitest";
import { t } from "../../../i18n";
import { certRefusal } from "./cert-refusal";

// What an ApiError of a 422 holds for one field.
const refused = (field: string, code: string, value?: string) => ({ codes: { [field]: code }, values: { [field]: value } });

describe("certRefusal", () => {
  it("names the field and says why in the CA's words", () => {
    const r = certRefusal(refused("domain", "cert_not_ready", "dns_elsewhere"), ["domain", "public_host"]);
    expect(r).toEqual({ field: "domain", code: "cert_not_ready", reason: t("errors.acme.dns_elsewhere") });
  });
  it("keeps a code it has no words for as it is, and reads the address too", () => {
    expect(certRefusal(refused("public_host", "cert_ordering"), ["domain", "public_host"])).toEqual({ field: "public_host", code: "cert_ordering", reason: "" });
    expect(certRefusal(refused("domain", "cert_not_ready", "new_code"), ["domain"])?.reason).toBe("new_code");
  });
  it("is nothing for another refusal, another field, or no refusal", () => {
    expect(certRefusal(refused("domain", "domain_elsewhere", "198.51.100.7"), ["domain"])).toBeNull();
    expect(certRefusal(refused("sub_domain", "cert_not_ready"), ["domain"])).toBeNull();
    expect(certRefusal(new Error("x"), ["domain"])).toBeNull();
    expect(certRefusal(undefined, ["domain"])).toBeNull();
  });
});
