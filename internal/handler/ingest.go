package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

func RegisterIngestion(devices *gin.RouterGroup, service *ingest.Service) {
	devices.POST("/readings", jsonResult(http.StatusAccepted, func(ctx context.Context, c *gin.Context) (gin.H, error) {
		var input ingest.Input
		if err := decode(c, &input); err != nil {
			return nil, err
		}
		if err := service.Submit(ctx, deviceIdentity(c), input); err != nil {
			return nil, err
		}
		return gin.H{"status": "queued", "event_id": input.EventID}, nil
	}))
}
