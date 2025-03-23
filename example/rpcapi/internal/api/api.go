package api

import (
	"fmt"
	"log"
	"os"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/go-playground/validator/v10"
)

// API struct holds our dependencies.
type API struct {
	Router       *gin.Engine // Export Router
	rabbitMQConn *amqp.Connection
	requestQueue string
	validate     *validator.Validate
	config       *Config
	logger       *log.Logger
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
		Router:       gin.Default(), // Exported
		rabbitMQConn: conn,
		requestQueue: config.RequestQueue,
		validate:     validate,
		config:       config,
		logger:       log.New(os.Stdout, "api: ", log.LstdFlags),
	}

	api.setupRoutes()
	api.setupMiddleware()
	return api, nil
}

// setupRoutes defines the API routes.
func (api *API) setupRoutes() {
	api.Router.POST("/report", api.reportHandler)          // Use Router
	api.Router.GET("/healthz", api.healthCheckHandler) // Use Router
}

// setupMiddleware adds middleware.
func (api *API) setupMiddleware() {
	api.Router.Use(api.requestLogger()) // Use Router
}

// Close cleans up resources.
func (api *API) Close() {
	if api.rabbitMQConn != nil {
		if err := api.rabbitMQConn.Close(); err != nil {
			api.logger.Printf("Error closing RabbitMQ connection: %v", err)
		}
	}
}
