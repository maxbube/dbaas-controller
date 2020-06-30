package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/percona-platform/dbaas-controller/logger"
)

// Context returns main application context with set logger
// that is canceled when SIGTERM or SIGINT is received.
func Context() context.Context {
	l := logger.NewLogger()

	ctx, cancel := context.WithCancel(context.Background())
	ctx = logger.GetCtxWithLogger(ctx, l)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-signals
		signal.Stop(signals)
		l.Warnf("Got %s, shutting down...", signalName(s))
		cancel()
	}()

	return ctx
}

func signalName(s os.Signal) string {
	switch s {
	case syscall.Signal(0x2):
		return "SIGINT"
	case syscall.Signal(0xf):
		return "SIGTERM"
	default:
		return ""
	}
}
