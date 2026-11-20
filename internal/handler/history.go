package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/history"
)

func historyFilter(c *gin.Context) (history.Filter, error) {
	var filter history.Filter
	var err error
	filter.Device, err = parseID(c.Param("id"))
	if err != nil {
		return filter, err
	}
	filter.Limit, filter.Offset, err = pagination(c)
	if err != nil {
		return filter, err
	}
	if sensor, ok := c.GetQuery("sensor_id"); ok {
		id, err := parseID(sensor)
		if err != nil {
			return filter, err
		}
		filter.Sensor = &id
	}
	for _, bound := range []struct {
		param string
		value *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if raw, ok := c.GetQuery(bound.param); ok {
			*bound.value, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil || bound.value.IsZero() {
				return filter, fmt.Errorf("%w: %s must be an RFC3339 timestamp", catalog.ErrInvalid, bound.param)
			}
		}
	}
	return filter, nil
}

func RegisterHistory(admin *gin.RouterGroup, service *history.Service) {
	admin.GET("/devices/:id/readings", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]history.Reading, error) {
		filter, err := historyFilter(c)
		if err != nil {
			return nil, err
		}
		return service.Readings(ctx, filter)
	}))
	admin.GET("/devices/:id/aggregations", jsonResult(http.StatusOK, func(ctx context.Context, c *gin.Context) ([]history.Bucket, error) {
		filter, err := historyFilter(c)
		if err != nil {
			return nil, err
		}
		return service.Aggregations(ctx, filter, c.DefaultQuery("bucket", "hour"))
	}))
}
