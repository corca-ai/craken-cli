package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/corca-ai/craken-cli/internal/craken"
)

var version = "dev"

func main() {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	go func() {
		select {
		case received := <-interrupts:
			cancel(&signalCancellation{signal: received})
		case <-ctx.Done():
		}
	}()
	if err := craken.Run(ctx, version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		craken.WriteError(os.Stderr, err, os.Args[1:])
		var interrupted *signalCancellation
		if errors.As(context.Cause(ctx), &interrupted) {
			if interrupted.signal == syscall.SIGTERM {
				os.Exit(143)
			}
			os.Exit(130)
		}
		os.Exit(1)
	}
}

type signalCancellation struct{ signal os.Signal }

func (s *signalCancellation) Error() string { return "interrupted by " + s.signal.String() }
