package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/httpapi"
	"agent-console/server/internal/identity"
	"agent-console/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, slog.Default()); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "create-user":
			return createUser(ctx, cfg.DBPath, args[1:], out)
		case "-h", "--help":
			fmt.Fprintln(out, "Usage: server [create-user --username NAME --password PASSWORD]")
			return nil
		default:
			return fmt.Errorf("unknown command %q", args[0])
		}
	}
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           httpapi.New(ctx, db, cfg, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("HTTP server listening", "addr", listener.Addr(), "provider", cfg.Provider)
	listening := make(chan error, 1)
	go func() { listening <- server.Serve(listener) }()
	select {
	case err := <-listening:
		return err
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		err := <-listening
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func createUser(ctx context.Context, path string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("create-user", flag.ContinueOnError)
	flags.SetOutput(out)
	username := flags.String("username", "", "user name")
	password := flags.String("password", "", "password (maximum 72 bytes)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	name := strings.TrimSpace(*username)
	if flags.NArg() != 0 || !identity.ValidUsername(name) || !identity.ValidPassword(*password) {
		return errors.New("use create-user --username NAME --password PASSWORD (1–72 bytes)")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.CreateUser(ctx, name, hash); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created user %q.\n", name)
	return nil
}
