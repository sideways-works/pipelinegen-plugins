-- +goose Up
-- +goose StatementBegin
CREATE TABLE tags (id int);
-- +goose StatementEnd
-- +goose Down
DROP TABLE tags;
