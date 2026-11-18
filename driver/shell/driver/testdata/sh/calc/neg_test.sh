. ./lib.sh

test_neg() {
	ran neg
	[ "$(neg 4)" = -4 ] || fail "neg 4 = $(neg 4), want -4"
}

test_neg_twice() {
	ran neg
	echo "neg of neg"
	[ "$(neg "$(neg 4)")" = 5 ] || fail "neg(neg 4) = $(neg "$(neg 4)"), want 5"
}
