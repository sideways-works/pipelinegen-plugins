-- migrate:up
CREATE TABLE notes (id int);

-- migrate:down
DROP TABLE notes;
