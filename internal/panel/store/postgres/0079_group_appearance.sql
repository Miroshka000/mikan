-- +goose Up
-- How a group's subscriptions look (#290, #276, #278): what a row holds goes over the panel's
-- settings for the group's clients, and a NULL is the panel's. The templates are the group's
-- of each core, the default of the core for its clients: a response rule that names one still
-- wins. The documents are JSON in TEXT, as the settings keep theirs: headers the group's own
-- answer headers ([{name, value}]), unbind_rules the subscriber's unbinding (domain.UnbindRules)
-- and page the subscription page (subpage.Page); the page's images and instructions stay the
-- panel's.
CREATE TABLE user_group_appearance (
  group_id         BIGINT PRIMARY KEY REFERENCES user_groups(id) ON DELETE CASCADE,
  template_mihomo  BIGINT REFERENCES sub_templates(id) ON DELETE SET NULL,
  template_singbox BIGINT REFERENCES sub_templates(id) ON DELETE SET NULL,
  template_xray    BIGINT REFERENCES sub_templates(id) ON DELETE SET NULL,
  title            TEXT,
  announce         TEXT,
  announce_url     TEXT,
  support_url      TEXT,
  update_hours     BIGINT CHECK (update_hours BETWEEN 1 AND 168),
  headers          TEXT NOT NULL DEFAULT '[]',
  require_hwid     BOOLEAN,
  unbind_rules     TEXT,
  page             TEXT
);

-- Where a group's clients reach an inbound in place of its default address: each field set
-- goes over what the default address gives (the node's address, the inbound's port, the
-- usual TLS name and Host, the inbound's name); cdn: a CDN in front shows its own certificate.
CREATE TABLE user_group_hosts (
  group_id   BIGINT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  inbound_id BIGINT NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
  address    TEXT    NOT NULL DEFAULT '',
  port       BIGINT  NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
  sni        TEXT    NOT NULL DEFAULT '',
  host       TEXT    NOT NULL DEFAULT '',
  name       TEXT    NOT NULL DEFAULT '' CHECK (char_length(name) <= 48),
  cdn        BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (group_id, inbound_id)
);
CREATE INDEX user_group_hosts_inbound ON user_group_hosts (inbound_id);

-- +goose Down
DROP TABLE user_group_hosts;
DROP TABLE user_group_appearance;
