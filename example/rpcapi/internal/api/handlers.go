package api

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"my-api/internal/models" // Import models

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
)

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

	var req models.ReportRequest
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

// randomString generates a random string of a given length (for correlation IDs).
func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}
