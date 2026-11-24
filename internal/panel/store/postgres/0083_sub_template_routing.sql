-- +goose Up
-- A simple subscription template (#276): a routing of its own in place of a text, as the
-- settings sub_routing, sub_routes and sub_rules are to the built-in profile
-- ({"mode", "routes", "rules"}). Its profiles are the built-in one of every core made by it.
-- It is kept as a Clash template, so that sing-box and xray with no default of their own take
-- it as they take a Clash one, and it has no text: a NULL routing is a template of text.
ALTER TABLE sub_templates ADD COLUMN routing JSONB
  CHECK (routing IS NULL OR jsonb_typeof(routing) = 'object' AND core = 'mihomo' AND body = '');

-- +goose Down
-- The releases before read every Clash template as text, and a simple one has none: the
-- profiles of sing-box and xray made of it would fail. The simple ones go; a group that chose
-- one gets its core's default, and a response rule that names one is left out of the policy.
DELETE FROM sub_templates WHERE routing IS NOT NULL;
ALTER TABLE sub_templates DROP COLUMN routing;
