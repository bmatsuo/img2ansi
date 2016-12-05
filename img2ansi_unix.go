//build: unix

package main

import (
	"os"

	"golang.org/x/crypto/ssh/terminal"
)

func getTermDim() (w, h int, err error) {
	return terminal.GetSize(int(os.Stdout.Fd()))
}
