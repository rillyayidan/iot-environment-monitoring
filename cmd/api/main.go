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

	"github.com/rillyayidan/iot-environment-monitoring/internal/config"
	"github.com/rillyayidan/iot-environment-monitoring/internal/database"
	"github.com/rillyayidan/iot-environment-monitoring/internal/router"
	"github.com/rillyayidan/iot-environment-monitoring/internal/stream"
)

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := database.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := stream.Open(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer client.Close()

	api := router.NewWithOptions(db, router.Options{
		AdminToken:     cfg.AdminToken,
		Producer:       stream.New(client, cfg.Stream, cfg.Group, cfg.QueueLimit),
		RedisPing:      func(ctx context.Context) error { return client.Ping(ctx).Err() },
		OfflineTimeout: cfg.OfflineTimeout,
	})
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: api, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close())
		}
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
