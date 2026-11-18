. ./lib.sh

test_add() {
	ran add
	[ "$(add 2 3)" = 5 ] || fail "add 2 3 = $(add 2 3), want 5"
}

test_add_negative() {
	ran add
	[ "$(add -2 3)" = 1 ] || fail "add -2 3 = $(add -2 3), want 1"
}

helper_not_a_test() {
	fail "never runs"
}
