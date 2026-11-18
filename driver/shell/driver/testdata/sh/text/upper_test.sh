test_upper() {
	[ -z "${PIPELINEGEN_RECORDING:-}" ] || echo upper >>"$PIPELINEGEN_RECORDING/ran"
	[ "$(echo abc | tr a-z A-Z)" = ABC ] || fail "upper abc"
}
