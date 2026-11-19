package router

import (
	"github.com/gin-gonic/gin"

	"github.com/rillyayidan/iot-environment-monitoring/internal/handler"
)

func New() *gin.Engine {
	r := gin.Default()

	r.GET("health/live", handler.HealthLive)

	return r
}
