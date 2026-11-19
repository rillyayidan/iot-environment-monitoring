package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func HealthLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "iot-environment-monitoring",
	})
}
