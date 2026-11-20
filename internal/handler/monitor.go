package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rillyayidan/iot-environment-monitoring/internal/monitor"
)

func RegisterDeviceStatus(admin *gin.RouterGroup, service *monitor.Service) {
	admin.GET("/devices/:id/status", byID(http.StatusOK, service.Status))
}

func RegisterHeartbeat(devices *gin.RouterGroup, service *monitor.Service) {
	devices.POST("/devices/heartbeat", func(c *gin.Context) {
		ctx, cancel := requestContext(c)
		defer cancel()
		if err := service.Heartbeat(ctx, deviceIdentity(c)); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
