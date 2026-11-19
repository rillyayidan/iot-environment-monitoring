package router

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rillyayidan/iot-environment-monitoring/internal/handler"
)

func New(db *pgxpool.Pool) *gin.Engine {
	r := gin.Default()

	r.GET("/health/live", handler.HealthLive)
	r.GET("/health/ready", handler.HealthReady(db))

	return r
}
