package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL     string
	AdminToken      string
	HTTPAddr        string
	RedisURL        string
	Stream          string
	Group           string
	QueueLimit      int
	WorkerCount     int
	WorkerBatchSize int
	ClaimIdle       time.Duration
	ProcessTimeout  time.Duration
	OfflineTimeout  time.Duration
	MonitorInterval time.Duration
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AdminToken:  os.Getenv("ADMIN_TOKEN"),
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		RedisURL:    env("REDIS_URL", "redis://127.0.0.1:6379/0"),
		Stream:      env("REDIS_STREAM", "iot:readings"),
		Group:       env("REDIS_GROUP", "iot-workers"),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	for _, setting := range []struct {
		target         *int
		name, fallback string
		min, max       int
	}{
		{&cfg.QueueLimit, "QUEUE_LIMIT", "10000", 1, 1000000},
		{&cfg.WorkerCount, "WORKER_COUNT", "4", 1, 64},
		{&cfg.WorkerBatchSize, "WORKER_BATCH_SIZE", "32", 1, 64},
	} {
		value, err := strconv.Atoi(env(setting.name, setting.fallback))
		if err != nil || value < setting.min || value > setting.max {
			return nil, fmt.Errorf("%s must be an integer between %d and %d", setting.name, setting.min, setting.max)
		}
		*setting.target = value
	}
	for _, setting := range []struct {
		target         *time.Duration
		name, fallback string
	}{
		{&cfg.ClaimIdle, "CLAIM_IDLE", "30s"},
		{&cfg.ProcessTimeout, "PROCESS_TIMEOUT", "10s"},
		{&cfg.OfflineTimeout, "OFFLINE_TIMEOUT", "5m"},
		{&cfg.MonitorInterval, "MONITOR_INTERVAL", "15s"},
	} {
		value, err := time.ParseDuration(env(setting.name, setting.fallback))
		if err != nil || value < time.Millisecond || value > 365*24*time.Hour {
			return nil, fmt.Errorf("%s must be a duration between 1ms and 365 days", setting.name)
		}
		*setting.target = value
	}
	if cfg.ClaimIdle <= cfg.ProcessTimeout {
		return nil, errors.New("CLAIM_IDLE must exceed PROCESS_TIMEOUT")
	}
	return cfg, nil
}
