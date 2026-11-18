CREATE EXTENSION IF NOT EXISTS citext;

CREATE TYPE note_state AS ENUM ('draft', 'published');

CREATE TABLE users (
  id bigserial PRIMARY KEY,
  email citext NOT NULL UNIQUE
);

CREATE TABLE notes (
  id bigserial PRIMARY KEY,
  author bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  title text NOT NULL CHECK (title <> ''),
  state note_state NOT NULL DEFAULT 'draft'
);

CREATE INDEX ON notes (author);

CREATE TABLE note_events (
  note_id bigint NOT NULL,
  at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION note_written() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO note_events (note_id) VALUES (NEW.id);
  RETURN NEW;
END $$;

CREATE TRIGGER note_written AFTER INSERT OR UPDATE ON notes
  FOR EACH ROW EXECUTE FUNCTION note_written();

CREATE VIEW published_notes AS
  SELECT n.id, n.title, u.email FROM notes n JOIN users u ON u.id = n.author
  WHERE n.state = 'published';

INSERT INTO users (email) VALUES ('first@example.com');
