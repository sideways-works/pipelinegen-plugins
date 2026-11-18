// Package orders makes and cancels orders.
package orders

import "example.com/shop/money"

// Order is one order.
type Order struct {
	Total money.Amount
	Paid  money.Kind
}

// Create makes an order of cents.
func Create(cents int) Order {
	money.Hook()

	return Order{Total: money.Gross(money.Amount{Cents: cents * money.Table["eur"] / 100}), Paid: money.Card}
}

// Price is the order's total in cents.
func (o Order) Price() int {
	return o.Total.Cents
}

// Cancel clears an order's total.
func Cancel(o Order) Order {
	if money.Total(o) == 0 {
		return o
	}

	o.Total = money.Amount{}

	return o
}
