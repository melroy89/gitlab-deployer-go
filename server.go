package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := loadDotEnv(); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	config, err := readConfig(os.Getenv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app, err := newApp(ctx, config)
	if err != nil {
		log.Fatalf("initialize: %v", err)
	}
	if app.processor != nil {
		app.processor.Start(ctx)
	}
	server := &http.Server{
		Addr: config.ListenAddress, Handler: app.routes(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		log.Printf("server listening on %s mode=%s", config.ListenAddress, config.Mode)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server failed: %v", err)
			cancel()
		}
	}()
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	if app.processor != nil {
		app.processor.Wait()
	}
}
