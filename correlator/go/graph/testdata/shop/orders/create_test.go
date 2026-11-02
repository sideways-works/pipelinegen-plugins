package orders

import "testing"

func TestCreate(t *testing.T) {
	if o := Create(100); o.Total.Cents != 119 {
		t.Fatal(o)
	}
}
