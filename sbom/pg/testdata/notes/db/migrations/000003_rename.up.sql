ALTER TABLE tags RENAME TO labels;
ALTER TABLE labels ADD CONSTRAINT labels_name_key UNIQUE (name);
ALTER TABLE notes DROP CONSTRAINT notes_title_check;
