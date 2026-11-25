-- +goose Up
-- Labels of a node and of an inbound, the admin's own words to sort and filter them by, as a
-- user has (users.tags): a JSON array of strings.
ALTER TABLE nodes ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
ALTER TABLE inbounds ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE inbounds DROP COLUMN tags;
ALTER TABLE nodes DROP COLUMN tags;
