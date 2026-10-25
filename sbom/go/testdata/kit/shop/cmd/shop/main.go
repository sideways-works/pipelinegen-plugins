// Command shop makes one order.
package main

import (
	"fmt"

	"example.com/shop/orders"
)

func main() {
	fmt.Println(orders.Create(100).Total)
}
