-- +goose Up
CREATE TABLE notes (id int);
-- +goose Down
DROP TABLE notes;
