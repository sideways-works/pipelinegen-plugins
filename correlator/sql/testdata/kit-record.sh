#!/bin/sh
# One part of the kit's slice, as a batch's tests leave it: statements the
# server logs (jsonlog) while the part runs, appended to the log the
# server writes from the batch's start (here kept in the fixture copy).
set -eu
log=source/pglog/server.json
now=$(date -u +"%Y-%m-%d %H:%M:%S.%3N UTC")
case "$PIPELINEGEN_PART" in
1) sql="SELECT id, title FROM notes WHERE id = 1" ;;
*) sql="INSERT INTO users (name) VALUES ('kit')" ;;
esac
printf '{"timestamp": "%s", "user": "app", "dbname": "notes_test_1", "pid": 101, "error_severity": "LOG", "message": "statement: %s", "backend_type": "client backend"}\n' "$now" "$sql" >>"$log"
