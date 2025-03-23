package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"my-api/internal/api" // Import your internal package
)

func main() {
	// Load configuration (using environment variables for simplicity)
	config := &api.Config{
		RabbitMQURL:  getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		RequestQueue: getEnv("REQUEST_QUEUE", "report_requests"), // Use a request queue name
		ServerPort:   getEnv("SERVER_PORT", "8080"),
	}

	app, err := api.NewAPI(config)
	if err != nil {
		log.Fatalf("Failed to create API: %v", err)
	}
	defer app.Close()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	srv := &http.Server{
		Addr:        ":" + config.ServerPort,
		Handler:     app.Router, // Use Router field
		ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("Starting server on :%s", config.ServerPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-quit // Wait for interrupt signal
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}
	log.Println("Server exiting")
}

// Helper function to get environment variables with default values
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
