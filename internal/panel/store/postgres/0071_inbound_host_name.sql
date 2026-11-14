-- +goose Up
-- A host's own name in the subscriptions (#01), with the variables of the profile title
-- ({days}, {left} and the rest) filled for each user. Empty: the inbound's name, with a
-- number after the default host's. The default host has it too, as its own: the template
-- holds no name.
ALTER TABLE inbound_hosts ADD COLUMN name TEXT NOT NULL DEFAULT '' CHECK (char_length(name) <= 48);

-- +goose Down
ALTER TABLE inbound_hosts DROP COLUMN name;
