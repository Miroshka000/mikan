-- +goose Up
-- Who a host is shown to (#01): the states of the subscriptions it goes to (empty: every
-- one; an expiring subscription is an active one) and the trial (only: to the trial's
-- subscriptions alone, never: to the others alone, '': to both). A host the user is not
-- shown is left out of the user's profile; the default host too.
ALTER TABLE inbound_hosts
  ADD COLUMN states TEXT[] NOT NULL DEFAULT '{}' CHECK (states <@ ARRAY['active', 'limited', 'expired', 'disabled']::text[]),
  ADD COLUMN trial  TEXT   NOT NULL DEFAULT ''   CHECK (trial IN ('', 'only', 'never'));

-- +goose Down
ALTER TABLE inbound_hosts DROP COLUMN states, DROP COLUMN trial;
