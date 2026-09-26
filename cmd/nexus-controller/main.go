package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/backendtest"
	"github.com/IniZio/nexus/internal/controller/chattest"
	"github.com/IniZio/nexus/internal/controller/storetest"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nexus-controller <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  serve   start the controller (fakes in use)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  -h, --help   show this help")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "nexus-controller:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "nexus-controller: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	exitAfter := fs.Duration("exit-after", 0, "exit after duration (0 = run until signal)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	slog.Info("nexus-controller: fakes in use", "adapters", "chat=chattest store=storetest backend=backendtest")

	chat := chattest.New()
	store := storetest.New()
	backend := backendtest.New()

	ctrl := controller.New(controller.Deps{
		Chat:      chat,
		Store:     store,
		Backend:   backend,
		Lifecycle: nil,
		Linker:    nil,
		Projects:  nil,
	})

	router := controller.NewRouter(store, ctrl)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *exitAfter > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *exitAfter)
		defer cancel()
	}

	if err := chat.Run(ctx, router.Handle); err != nil {
		return err
	}

	router.Close()

	_ = backend
	return nil
}
