package main

import (
	"fmt"
	"os"

	"github.com/wsq2333/curvine-operator/internal/cachenode"
)

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--read-probe" {
		c, e := cachenode.Load(cachenode.ConfigPath)
		err = e
		if err == nil {
			err = cachenode.VerifyMountedFile(c)
		}
	} else if len(os.Args) == 1 {
		err = cachenode.Run()
	} else {
		err = fmt.Errorf("unsupported arguments")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cache node operation failed")
		os.Exit(1)
	}
}
