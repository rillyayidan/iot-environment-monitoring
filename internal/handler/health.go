package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func HealthLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "iot-environment-monitoring",
	})
}

func HealthReady(db *pgxpool.Pool, dependencies ...func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(
			c.Request.Context(),
			2*time.Second,
		)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			serviceUnavailable(c, gin.H{"status": "unavailable"})
			return
		}

		for _, ping := range dependencies {
			if err := ping(ctx); err != nil {
				serviceUnavailable(c, gin.H{"status": "unavailable"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"status": "ready",
		})
	}
}
