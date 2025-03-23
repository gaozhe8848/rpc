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
    "math/rand"
    "strconv"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/go-playground/validator/v10"
)

// Config holds application configuration.
type Config struct {
	RabbitMQURL  string `validate:"required"`
	RequestQueue string `validate:"required"` // Queue for sending requests
	ServerPort   string `validate:"required"`
}

// API struct holds our dependencies.
type API struct {
	router       *gin.Engine
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
		router:       gin.Default(),
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
	api.router.POST("/report", api.reportHandler) // Changed route to /report
	api.router.GET("/healthz", api.healthCheckHandler)
}

// setupMiddleware adds middleware.
func (api *API) setupMiddleware() {
	api.router.Use(api.requestLogger())
}
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
// ReportRequest struct for the request body
type ReportRequest struct {
	ReportID string `json:"report_id" validate:"required"`
	// Add other request parameters as needed
}

// reportHandler handles requests for reports (using RabbitMQ RPC).
func (api *API) reportHandler(c *gin.Context) {
    // --- 1. Prepare for RPC ---
    ch, err := api.rabbitMQConn.Channel()
    if err != nil {
        api.logger.Printf("Failed to open a channel: %v", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open a channel"})
        return
    }
    defer ch.Close()

    // Declare a reply queue (exclusive to this consumer)
    replyQueue, err := ch.QueueDeclare(
        "",    // Name (empty for server-generated name)
        false, // Durable
        false, // Delete when unused
        true,  // Exclusive (only this connection can access)
        false, // No-wait
        nil,   // Arguments
    )
    if err != nil {
        api.logger.Printf("Failed to declare a reply queue: %v", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to declare reply queue"})
        return
    }

    // Start consuming from the reply queue *before* sending the request
    msgs, err := ch.Consume(
        replyQueue.Name, // Queue name
        "",            // Consumer tag (auto-generated)
        true,          // Auto-ack (important for RPC)
        false,         // Exclusive
        false,         // No-local
        false,         // No-wait
        nil,           // Args
    )
    if err != nil {
        api.logger.Printf("Failed to register a consumer: %v", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register consumer"})
        return
    }

    // Generate a unique correlation ID
    corrID := randomString(32)


    // --- 2. Bind and Validate Request ---

    var req ReportRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        api.logger.Printf("Invalid request body: %v", err)
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
        return
    }
    if err := api.validate.Struct(req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    // --- 3. Publish the Request Message ---
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Timeout for publishing
	defer cancel()

    err = ch.PublishWithContext(ctx,
        "",               // Exchange (default)
        api.requestQueue, // Routing key (request queue)
        false,            // Mandatory
        false,            // Immediate
        amqp.Publishing{
            ContentType:   "application/json", // Or appropriate content type
            CorrelationId: corrID,             // Set the correlation ID
            ReplyTo:       replyQueue.Name,     // Set the reply-to queue
            Body:          []byte(fmt.Sprintf(`{"report_id": "%s"}`, req.ReportID)), // Example: Send report ID as JSON
        })
    if err != nil {
        api.logger.Printf("Failed to publish a message: %v", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to publish request"})
        return
    }

     // --- 4. Wait for the Response (with Timeout) ---
    select {
        case d := <-msgs:
            if corrID == d.CorrelationId {
                // --- 5. Process Response and Send to Client ---
                //  In a real application, you'd likely unmarshal the response body
                //  into a struct representing the report.
                c.Data(http.StatusOK, d.ContentType, d.Body) // Send report data back to client
            } else {
                api.logger.Printf("Received message with incorrect correlation ID: %s", d.CorrelationId)
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Mismatched correlation ID"})
            }
        case <-time.After(15 * time.Second): // Timeout for the entire RPC call
            api.logger.Println("Timeout waiting for report response")
            c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout waiting for report"})

    }
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
		RabbitMQURL:  getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		RequestQueue: getEnv("REQUEST_QUEUE", "report_requests"), // Use a request queue name
		ServerPort:   getEnv("SERVER_PORT", "8080"),
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
		Addr:        ":" + config.ServerPort,
		Handler:     api.router,
		ReadTimeout: 10 * time.Second, // Add ReadTimeout
        WriteTimeout: 10 * time.Second, //Add WriteTimeout
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

// randomString generates a random string of a given length (for correlation IDs).
func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}
