package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yeboahd24/cronwatch/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := errors.AsType[*app.ExitError](err); ok {
			if exit.Code > 0 {
				os.Exit(exit.Code)
			}
			os.Exit(1)
		}
		os.Exit(1)
	}
}
