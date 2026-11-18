# Passes: adds two numbers. Under coverage it writes its own LCOV record of
# calc.sh into COVERAGE_DIR, as an instrumented test would.
. calc/calc.sh
[ "$(add 2 3)" = 5 ] || { echo "add 2 3 is $(add 2 3), not 5"; exit 1; }
if [ -n "${COVERAGE_DIR-}" ]; then
	printf 'SF:calc/calc.sh\nFN:2,add\nFNDA:1,add\nDA:2,1\nDA:3,0\nend_of_record\n' >"$COVERAGE_DIR/add_test.dat"
fi
echo "add: ok"
