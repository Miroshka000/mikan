// A group of clients as the admin panel holds it (#290): what the group sees. A client is in
// its own group or else its tariff's; a group's servers, when it chose them, stand in for the
// tariff's, and the card's own protocols stay over both.
import type { Inbound, Schemas } from "../../../api/client";
import { serversHas } from "../sale-rules";
import type { ServersDraft } from "../servers-tree";

export type Group = Schemas["GroupView"];

/** A group's servers as the form holds them: `all` is "as in the client's tariff". */
export function groupServersDraft(g?: Pick<Group, "servers">): ServersDraft {
  return { all: g?.servers.as_tariff ?? true, nodes: new Set(g?.servers.nodes ?? []), inbounds: new Set(g?.servers.inbounds ?? []) };
}

export function groupServersBody(d: ServersDraft): Schemas["GroupServersBody"] {
  return d.all ? { as_tariff: true } : { as_tariff: false, nodes: [...d.nodes], inbounds: [...d.inbounds] };
}

/** Whether a group of these servers includes the protocol; false while it leaves them to the tariff. */
export function groupHas(g: Pick<Group, "servers">, i: Inbound): boolean {
  return !g.servers.as_tariff && serversHas({ all: false, nodes: g.servers.nodes, inbounds: g.servers.inbounds }, i);
}
