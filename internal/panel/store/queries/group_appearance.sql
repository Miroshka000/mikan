-- name: ListGroupAppearance :many
-- Every group's appearance but its page, which only the group's subscription page reads:
-- has_page says it has one.
SELECT group_id, template_mihomo, template_singbox, template_xray, title, announce, announce_url, support_url,
       update_hours, headers, require_hwid, unbind_rules, (page IS NOT NULL)::boolean AS has_page
FROM user_group_appearance ORDER BY group_id;

-- name: SetGroupAppearance :exec
-- The group's appearance but its page (SetGroupPage).
INSERT INTO user_group_appearance (group_id, template_mihomo, template_singbox, template_xray, title, announce, announce_url,
  support_url, update_hours, headers, require_hwid, unbind_rules)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (group_id) DO UPDATE SET template_mihomo = excluded.template_mihomo, template_singbox = excluded.template_singbox,
  template_xray = excluded.template_xray, title = excluded.title, announce = excluded.announce, announce_url = excluded.announce_url,
  support_url = excluded.support_url, update_hours = excluded.update_hours, headers = excluded.headers,
  require_hwid = excluded.require_hwid, unbind_rules = excluded.unbind_rules;

-- name: GetGroupUnbindRules :one
SELECT unbind_rules FROM user_group_appearance WHERE group_id = $1;

-- name: GetGroupPage :one
SELECT page FROM user_group_appearance WHERE group_id = $1;

-- name: SetGroupPage :exec
-- The group's own subscription page; NULL: the panel's again.
INSERT INTO user_group_appearance (group_id, page) VALUES (sqlc.arg(group_id), sqlc.narg(page))
ON CONFLICT (group_id) DO UPDATE SET page = excluded.page;

-- name: ListGroupHosts :many
-- Where each group's clients reach an inbound in place of its default address.
SELECT * FROM user_group_hosts ORDER BY group_id, inbound_id;

-- name: ClearGroupHosts :exec
DELETE FROM user_group_hosts WHERE group_id = $1;

-- name: AddGroupHost :exec
INSERT INTO user_group_hosts (group_id, inbound_id, address, port, sni, host, name, cdn)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
