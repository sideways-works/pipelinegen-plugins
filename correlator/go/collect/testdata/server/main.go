// Command server runs a or b for each line it reads, and answers each line
// with "ok", until its input ends.
package main

import (
	"bufio"
	"fmt"
	"os"
)

func a() { fmt.Println("ok a") }

func b() { fmt.Println("ok b") }

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch sc.Text() {
		case "a":
			a()
		case "b":
			b()
		default:
			fmt.Println("ok")
		}
	}
}
