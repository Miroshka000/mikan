-- +goose Up
-- What the admin tells buyers about a tariff in their own words: the limits do not show
-- what differs between tariffs with the same numbers (with white lists, without). Plain
-- text of up to 300 characters and 5 lines, checked by the domain; empty: nothing to add
-- to the line the panel builds from the limits.
ALTER TABLE tariffs ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tariffs DROP COLUMN description;
