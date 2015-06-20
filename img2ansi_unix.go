//build: unix

package main

import (
	"os"

	"code.google.com/p/gosshold/ssh/terminal"
)

func getTermDim() (w, h int, err error) {
	return terminal.GetSize(int(os.Stdout.Fd()))
}
