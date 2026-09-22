package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
	"natbox/internal/auth"
)

func main() {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		f = os.Stdin
	}
	if f != os.Stdin {
		defer f.Close()
	}
	fmt.Fprint(os.Stdout, "New Natbox admin password (12+ chars): ")
	password, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	hash, err := auth.HashPassword(string(password))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
