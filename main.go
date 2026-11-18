package main

import (
	"os"

	"github.com/werewolfboy13/filebrowser/v2/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
