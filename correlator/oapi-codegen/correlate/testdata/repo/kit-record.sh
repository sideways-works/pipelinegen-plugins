#!/bin/sh
# One part of the kit's slice, as a batch's tests leave it: the requests
# the stack served while the part ran, appended to the request log it
# writes from the batch's start (here kept in the fixture copy).
set -eu
case "$PIPELINEGEN_PART" in
1) echo "GET /api/notes/n1" ;;
*) echo "POST /api/notes" ;;
esac >>source/requests.log
