package main

import (
	"context"
	"log"

	"github.com/rillyayidan/iot-environment-monitoring/internal/config"
	"github.com/rillyayidan/iot-environment-monitoring/internal/database"
	"github.com/rillyayidan/iot-environment-monitoring/internal/router"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	db, err := database.NewPostgres(
		context.Background(),
		cfg.DatabaseURL,
	)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	log.Println("PostgreSQL connected successfully")

	r := router.New(db)

	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
