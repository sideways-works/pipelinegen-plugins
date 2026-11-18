// Package money holds amounts and their rates.
package money

import "fmt"

// Rate is the tax rate in percent.
const Rate = 19

// Kind is how an amount is paid.
type Kind int

// The kinds of payment.
const (
	Cash Kind = iota
	Card
)

// Amount is money in cents.
type Amount struct {
	Cents int `json:"cents"`
}

// Currency is the currency amounts are in.
var Currency = fmt.Sprintf("%s", "EUR")

// Table holds the currencies' minor units.
var Table = build()

func build() map[string]int { return map[string]int{"eur": 100} }

// String prints the cents.
func (a Amount) String() string { return fmt.Sprintf("%d", a.Cents) }

// Gross adds the tax.
func Gross(a Amount) Amount {
	_ = Currency

	return Amount{Cents: a.Cents * (100 + Rate) / 100}
}

// Hook is called on every order; it does nothing yet.
func Hook() {}

// Pricer is anything with a price.
type Pricer interface {
	Price() int
}

// Total adds up prices.
func Total(ps ...Pricer) int {
	t := 0
	for _, p := range ps {
		t += p.Price()
	}

	return t
}

// Unused is called by nothing.
func Unused() int {
	return Rate * 2
}
