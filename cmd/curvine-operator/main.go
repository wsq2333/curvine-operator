package main

import (
	"github.com/wsq2333/curvine-operator/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
