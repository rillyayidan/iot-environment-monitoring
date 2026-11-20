package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func RegisterCatalog(admin *gin.RouterGroup, service *catalog.Service) {
	admin.POST("/devices", noStore, withBody(http.StatusCreated, service.RegisterDevice))
	admin.GET("/devices", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]catalog.Device, error) {
		limit, offset, err := pagination(c)
		if err != nil {
			return nil, err
		}
		return service.ListDevices(ctx, limit, offset)
	}))
	admin.GET("/devices/:id", byID(http.StatusOK, service.Device))
	admin.PATCH("/devices/:id", withIDBody(http.StatusOK, service.PatchDevice))
	admin.POST("/devices/:id/identifiers/rotate", byID(http.StatusOK, service.RotateDeviceID))
	admin.POST("/devices/:id/keys/rotate", noStore, byID(http.StatusOK, service.RotateKey))

	admin.POST("/devices/:id/sensors", withIDBody(http.StatusCreated, service.RegisterSensor))
	admin.GET("/devices/:id/sensors", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]catalog.Sensor, error) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			return nil, err
		}
		limit, offset, err := pagination(c)
		if err != nil {
			return nil, err
		}
		return service.ListSensors(ctx, id, limit, offset)
	}))
	admin.GET("/sensors/:id", byID(http.StatusOK, service.Sensor))
	admin.PATCH("/sensors/:id", withIDBody(http.StatusOK, service.PatchSensor))
	admin.POST("/sensors/:id/identifiers/rotate", byID(http.StatusOK, service.RotateSensorID))
}
