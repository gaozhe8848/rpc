package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/go-playground/validator/v10" // for input validate
)

// Config holds application configuration.
type Config struct {
	RabbitMQURL string `validate:"required"`
	QueueName   string `validate:"required"`
	ServerPort  string `validate:"required"`
}

// API struct holds our dependencies.
type API struct {
	router     *gin.Engine
	rabbitMQConn *amqp.Connection
	queueName  string
	validate *validator.Validate
	config   *Config
	logger   *log.Logger // Use standard library logger as an example
}

// NewAPI creates a new API instance.
func NewAPI(config *Config) (*API, error) {

	// Validate Config
	validate := validator.New()
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	conn, err := amqp.Dial(config.RabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}


	api := &API{
		router:     gin.Default(),
		rabbitMQConn: conn,
		queueName:  config.QueueName,
		validate: validate,
		config:   config,
		logger:   log.New(os.Stdout, "api: ", log.LstdFlags), // Simple logger
	}

	api.setupRoutes()
	api.setupMiddleware() // Add middleware setup
	return api, nil
}

// setupRoutes defines the API routes.
func (api *API) setupRoutes() {
	api.router.POST("/publish", api.publishHandler)
	api.router.GET("/healthz", api.healthCheckHandler) // Health check endpoint
}

// setupMiddleware adds middleware to the Gin engine.
func (api *API) setupMiddleware() {
	api.router.Use(api.requestLogger()) // Custom request logging middleware
}

// requestLogger is a middleware for logging requests.
func (api *API) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next() // Process request

		// Log details after the request is handled
		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()

		if raw != "" {
			path = path + "?" + raw
		}

		api.logger.Printf("[GIN] %s | %3d | %13v | %15s | %-7s %s",
			time.Now().Format(time.RFC3339),
			statusCode,
			latency,
			clientIP,
			method,
			path,
		)
	}
}
// Message struct for request body with validation
type Message struct {
	Message string `json:"message" validate:"required,min=1,max=1024"`
}

// publishHandler handles requests to publish a message.
func (api *API) publishHandler(c *gin.Context) {
	ch, err := api.rabbitMQConn.Channel()
	if err != nil {
		api.logger.Printf("Failed to open a channel: %v", err) // Log the error
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open a channel"})
		return
	}
	defer ch.Close()

	_, err = ch.QueueDeclare(
		api.queueName, // name
		true,          // durable
		false,         // delete when unused
		false,         // exclusive
		false,         // no-wait
		nil,           // arguments
	)
	if err != nil {
		api.logger.Printf("Failed to declare a queue: %v", err) // Log error
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to declare a queue"})
		return
	}

	var msg Message
	if err := c.ShouldBindJSON(&msg); err != nil {
		api.logger.Printf("Invalid request body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Validate message
    if err := api.validate.Struct(msg); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

	err = ch.Publish(
		"",           // exchange
		api.queueName, // routing key (queue name)
		false,        // mandatory
		false,        // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        []byte(msg.Message),
		})
	if err != nil {
		api.logger.Printf("Failed to publish message: %v", err) // Log error
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to publish message"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "message published"})
}

// healthCheckHandler checks the health of the application.
func (api *API) healthCheckHandler(c *gin.Context) {
	// Check RabbitMQ connection (basic check)
	if api.rabbitMQConn.IsClosed() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "reason": "rabbitmq_disconnected"})
		return
	}
	//Could perform a more robust check, e.g., by publishing and consuming a test message.

	c.JSON(http.StatusOK, gin.H{"status": "healthy"})
}
// Close cleans up resources.
func (api *API) Close() {
	if api.rabbitMQConn != nil {
		if err := api.rabbitMQConn.Close(); err != nil {
            api.logger.Printf("Error closing RabbitMQ connection: %v", err)
        }
	}
}

func main() {
	// Load configuration (using environment variables for simplicity)
	config := &Config{
		RabbitMQURL: getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		QueueName:   getEnv("QUEUE_NAME", "my_queue"),
		ServerPort:  getEnv("SERVER_PORT", "8080"),
	}

	api, err := NewAPI(config)
	if err != nil {
		log.Fatalf("Failed to create API: %v", err)
	}
	defer api.Close()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	srv := &http.Server{
		Addr:    ":" + config.ServerPort,
		Handler: api.router,
	}

	go func() {
		api.logger.Printf("Starting server on :%s", config.ServerPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-quit // Wait for interrupt signal
	api.logger.Println("Shutting down server...")

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		api.logger.Fatal("Server forced to shutdown: ", err)
	}
	api.logger.Println("Server exiting")

}
// Helper function to get environment variables with default values
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
