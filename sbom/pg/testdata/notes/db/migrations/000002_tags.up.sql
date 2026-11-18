CREATE TABLE tags (
  id serial PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE note_tags (
  note_id bigint NOT NULL REFERENCES notes (id),
  tag_id int NOT NULL REFERENCES tags (id),
  PRIMARY KEY (note_id, tag_id)
);

CREATE FUNCTION tag_count(note bigint) RETURNS bigint LANGUAGE sql STABLE AS
  'SELECT count(*) FROM note_tags WHERE note_id = note';

-- a check, not a change
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM notes WHERE title = '') THEN
    RAISE EXCEPTION 'empty titles';
  END IF;
END $$;
