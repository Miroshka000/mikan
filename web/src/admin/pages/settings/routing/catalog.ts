import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "../../../../api/client";

/** The services and the sets of direct apps the routing can send somewhere: the same for every panel of a release. */
export function useCatalog() {
  return useQuery({ queryKey: ["routes-catalog"], queryFn: ({ signal }) => unwrap(api.GET("/api/v1/settings/routes/catalog", { signal })), staleTime: Infinity });
}
