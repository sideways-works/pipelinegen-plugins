-- What each installed extension provides, as model/extensions.txt lists it.
-- Install the extensions into an empty database of the PostgreSQL and
-- extension versions to describe, then:
--
--   psql -d <database> -AtF ' ' -f model/extensions.sql | grep -v '^plpgsql ' > lines
--
-- and put the header line of model/extensions.txt, with the versions,
-- above them.
SELECT DISTINCT x.extname, 'function', p.proname
FROM pg_depend d JOIN pg_extension x ON x.oid = d.refobjid JOIN pg_proc p ON p.oid = d.objid
WHERE d.classid = 'pg_proc'::regclass AND d.deptype = 'e'
UNION
SELECT DISTINCT x.extname, 'type', t.typname
FROM pg_depend d JOIN pg_extension x ON x.oid = d.refobjid JOIN pg_type t ON t.oid = d.objid
WHERE d.classid = 'pg_type'::regclass AND d.deptype = 'e' AND t.typname !~ '^_'
UNION
SELECT DISTINCT x.extname, 'opclass', o.opcname
FROM pg_depend d JOIN pg_extension x ON x.oid = d.refobjid JOIN pg_opclass o ON o.oid = d.objid
WHERE d.classid = 'pg_opclass'::regclass AND d.deptype = 'e'
ORDER BY 1, 2, 3;
