# Passes: upper-cases a word. Under coverage it writes its own LCOV record
# of text.sh into COVERAGE_DIR, as an instrumented test would.
. text/text.sh
[ "$(upper word)" = WORD ] || { echo "upper word is $(upper word), not WORD"; exit 1; }
if [ -n "${COVERAGE_DIR-}" ]; then
	printf 'SF:text/text.sh\nFN:2,upper\nFNDA:1,upper\nDA:2,1\nend_of_record\n' >"$COVERAGE_DIR/upper_test.dat"
fi
echo "upper: ok"
