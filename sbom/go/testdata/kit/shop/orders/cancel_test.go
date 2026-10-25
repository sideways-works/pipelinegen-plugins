package orders

import "testing"

func TestCancel(t *testing.T) {
	if o := Cancel(Order{}); o.Total.Cents != 0 {
		t.Fatal(o)
	}
}
