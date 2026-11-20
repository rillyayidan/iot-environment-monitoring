package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/rillyayidan/iot-environment-monitoring/internal/alert"
	"github.com/rillyayidan/iot-environment-monitoring/internal/config"
	"github.com/rillyayidan/iot-environment-monitoring/internal/database"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
	"github.com/rillyayidan/iot-environment-monitoring/internal/monitor"
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

	queue := stream.New(client, cfg.Stream, cfg.Group, cfg.QueueLimit)
	monitoring := monitor.NewPostgres(db, cfg.OfflineTimeout)
	processor := ingest.NewPostgres(db, alert.NewPostgres(db), monitoring)
	var tasks sync.WaitGroup
	tasks.Go(func() { monitor.New(monitoring).Run(ctx, cfg.MonitorInterval) })
	stream.NewWorker(queue, processor, cfg.WorkerCount, cfg.WorkerBatchSize, cfg.ClaimIdle, cfg.ProcessTimeout).Run(ctx)
	tasks.Wait()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
