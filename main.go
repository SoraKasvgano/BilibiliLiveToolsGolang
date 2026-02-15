package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bilibililivetools/gover/backend/app"
	"bilibililivetools/gover/backend/config"
)

//go:embed frontend
var frontendFS embed.FS

func main() {
	cfgManager, err := config.NewManager()
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	application, err := app.New(cfgManager, frontendFS)
	if err != nil {
		log.Fatalf("init app failed: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received signal: %s", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := application.Shutdown(ctx); err != nil {
			log.Fatalf("shutdown failed: %v", err)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server stopped with error: %v", err)
		}
	}
}
