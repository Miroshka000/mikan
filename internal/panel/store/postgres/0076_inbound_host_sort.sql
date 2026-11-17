-- +goose Up
-- The place of every address for clients in the subscriptions (#300): the servers of all the
-- nodes go in this one order, which the admin sets by dragging, and a node goes where its
-- first server does. The places start as the subscriptions had them: the nodes in their
-- order, a node's inbounds by id, an inbound's hosts by position, so a panel that never
-- reorders sees no change. A new host takes the place of its inbound's last one, a new
-- inbound that of its node's last server, and a tie goes as before: by node, inbound and
-- position (queries/hosts.sql).
ALTER TABLE inbound_hosts ADD COLUMN sort BIGINT NOT NULL DEFAULT 0;
UPDATE inbound_hosts SET sort = ranked.pos FROM (
  SELECT h.id, ROW_NUMBER() OVER (ORDER BY n.sort, n.id, i.id, h.position) AS pos
  FROM inbound_hosts h JOIN inbounds i ON i.id = h.inbound_id JOIN nodes n ON n.id = i.node_id
) AS ranked WHERE inbound_hosts.id = ranked.id;

-- +goose Down
ALTER TABLE inbound_hosts DROP COLUMN sort;
