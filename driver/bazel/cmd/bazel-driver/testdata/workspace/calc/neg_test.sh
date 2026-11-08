# Fails on purpose: its expectation is wrong, so the kit compares a failed
# slice's result one by one and bundled too.
. calc/calc.sh
if [ -n "${COVERAGE_DIR-}" ]; then
	printf 'SF:calc/calc.sh\nFN:3,neg\nFNDA:1,neg\nDA:2,0\nDA:3,1\nend_of_record\n' >"$COVERAGE_DIR/neg_test.dat"
fi
[ "$(neg 4)" = 4 ] || { echo "neg 4 is $(neg 4), not 4"; exit 1; }
