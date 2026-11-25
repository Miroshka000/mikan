-- +goose Up
-- The admin's own front page (frontpage.Custom), made in the panel's builder, next to the
-- built-in ones.
ALTER TABLE nodes DROP CONSTRAINT nodes_front_page_check;
ALTER TABLE nodes ADD CONSTRAINT nodes_front_page_check CHECK (front_page IN ('', 'maintenance', 'cats', 'custom'));

-- +goose Down
UPDATE nodes SET front_page = '' WHERE front_page = 'custom';
ALTER TABLE nodes DROP CONSTRAINT nodes_front_page_check;
ALTER TABLE nodes ADD CONSTRAINT nodes_front_page_check CHECK (front_page IN ('', 'maintenance', 'cats'));
