-- +goose Up
CREATE TABLE payment_access_traffic (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expired_at bigint NOT NULL,
    bytes bigint NOT NULL,
    PRIMARY KEY (user_id, expired_at)
);

-- +goose Down
DROP TABLE payment_access_traffic;
