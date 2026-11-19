package main

import (
	"log"

	"github.com/rillyayidan/iot-environment-monitoring/internal/router"
)

func main() {
	r := router.New()

	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
