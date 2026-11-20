package router

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rillyayidan/iot-environment-monitoring/internal/alert"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/handler"
	"github.com/rillyayidan/iot-environment-monitoring/internal/history"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
	"github.com/rillyayidan/iot-environment-monitoring/internal/monitor"
)

type Options struct {
	AdminToken     string
	Producer       ingest.Producer
	RedisPing      func(context.Context) error
	OfflineTimeout time.Duration
}

func New(db *pgxpool.Pool) *gin.Engine {
	return NewWithOptions(db, Options{})
}

func NewWithOptions(db *pgxpool.Pool, options Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		slog.Error("request panic", "route", c.FullPath())
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}))
	r.GET("/health/live", handler.HealthLive)
	if options.RedisPing != nil {
		r.GET("/health/ready", handler.HealthReady(db, options.RedisPing))
	} else {
		r.GET("/health/ready", handler.HealthReady(db))
	}

	service := catalog.New(catalog.NewPostgres(db))
	if options.OfflineTimeout == 0 {
		options.OfflineTimeout = 5 * time.Minute
	}
	monitoring := monitor.New(monitor.NewPostgres(db, options.OfflineTimeout))
	admin := r.Group("/api/v1", handler.Admin(options.AdminToken))
	handler.RegisterCatalog(admin, service)
	handler.RegisterHistory(admin, history.New(history.NewPostgres(db)))
	handler.RegisterAlerts(admin, alert.New(alert.NewPostgres(db)))
	handler.RegisterDeviceStatus(admin, monitoring)
	devices := r.Group("/api/v1", handler.DeviceAuth(service))
	handler.RegisterHeartbeat(devices, monitoring)
	if options.Producer != nil {
		handler.RegisterIngestion(devices, ingest.New(ingest.NewPostgres(db, nil, nil), options.Producer))
	}
	return r
}
