package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"dotm/internal/cli"
)

func main() {
	defer func() {
		if cause := recover(); cause != nil {
			fmt.Fprintf(os.Stderr, "dotm: internal panic: %v\n%s", cause, debug.Stack())
			os.Exit(3)
		}
	}()
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		cli.PrintError(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
