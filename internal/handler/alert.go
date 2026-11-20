package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rillyayidan/iot-environment-monitoring/internal/alert"
)

func RegisterAlerts(admin *gin.RouterGroup, service *alert.Service) {
	admin.POST("/sensors/:id/alert-rules", withIDBody(http.StatusCreated, service.CreateRule))
	admin.GET("/sensors/:id/alert-rules", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]alert.Rule, error) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			return nil, err
		}
		limit, offset, err := pagination(c)
		if err != nil {
			return nil, err
		}
		return service.ListRules(ctx, id, limit, offset)
	}))
	admin.PATCH("/alert-rules/:id", withIDBody(http.StatusOK, service.PatchRule))
	admin.GET("/alerts", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]alert.Alert, error) {
		limit, offset, err := pagination(c)
		if err != nil {
			return nil, err
		}
		return service.List(ctx, c.Query("status"), limit, offset)
	}))
	admin.GET("/alerts/:id", byID(http.StatusOK, service.Get))
}
