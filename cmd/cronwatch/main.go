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
		// A child's failure is already in its own output; printing it again
		// would add noise to cron mail.
		if exit, ok := errors.AsType[*app.ExitError](err); ok && exit.Code > 0 {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
