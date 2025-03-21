package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin" // Using Gin for the web framework
	amqp "github.com/rabbitmq/amqp091-go"
)

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %v", msg, err)
	}
}

func generateCorrelationID() string {
	rand.Seed(time.Now().UnixNano())
	return strconv.Itoa(rand.Int())
}

func setupRabbitMQ() (*amqp.Connection, *amqp.Channel) {
	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	failOnError(err, "Failed to connect to RabbitMQ")

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	return conn, ch
}

func handleGenerateReportRequest(c *gin.Context, ch *amqp.Channel) {
	// 1. Declare the queue where Server A is listening for requests
	serverAQueueName := "report_requests"

	// 2. Declare a reply queue for this client
	replyQueue, err := ch.QueueDeclare(
		"",    // Auto-generated queue name
		false, // Durable
		true,  // Delete when unused
		true,  // Exclusive
		false, // No-wait
		nil,   // Arguments
	)
	failOnError(err, "Failed to declare reply queue")

	// 3. Generate a unique ID for this request
	correlationID := generateCorrelationID()

	// 4. Prepare the request payload
	request := map[string]interface{}{
		"request_id": "report_123",
		"start_date": "2023-01-01",
		"end_date":   "2023-12-31",
	}
	body, err := json.Marshal(request)
	failOnError(err, "Failed to marshal request")

	// 5. Create a context
	ctx := context.Background()

	// 6. Publish the request to Server A's queue
	err = ch.PublishWithContext(ctx,
		"",               // Exchange
		serverAQueueName, // Routing key: Server A's queue name
		false,            // Mandatory
		false,            // Immediate
		amqp.Publishing{
			ContentType:   "application/json",
			CorrelationId: correlationID,   // Set correlation ID
			ReplyTo:       replyQueue.Name, // Set reply queue
			Body:          body,            // Request body
		})
	failOnError(err, "Failed to publish request")

	log.Printf(" [x] Sent request to Server A, correlation ID: %s\n", correlationID)

	// 7. Consume the response from the reply queue
	msgs, err := ch.Consume(
		replyQueue.Name,
		"",
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to consume response")

	// 8. Wait for and process the response (with a timeout)
	select {
	case msg, ok := <-msgs:
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to receive response from RabbitMQ",
			})
			return
		}
		if msg.CorrelationId == correlationID { // Check correlation ID
			log.Printf(" [x] Received response: %v\n", string(msg.Body))
			// In a real application, you would unmarshal the response and process the data
			var responseData map[string]interface{}
			err = json.Unmarshal(msg.Body, &responseData)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to unmarshal response body",
				})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "Report generated successfully",
				"data":    responseData,
			})
			return // Exit after receiving the expected response
		} else {
			log.Printf(" [x] Received message with unexpected correlation ID: %s, expected %s\n", msg.CorrelationId, correlationID)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Unexpected correlation ID",
			})
			return
		}
	case <-time.After(10 * time.Second): // 10-second timeout
		c.JSON(http.StatusGatewayTimeout, gin.H{
			"error": "Timeout waiting for response from Server A",
		})
		return
	}
}

func main() {
	// Setup RabbitMQ connection and channel
	conn, ch := setupRabbitMQ()
	defer conn.Close()
	defer ch.Close()

	// Setup Gin router
	router := gin.Default()

	// Define the GET /report endpoint
	router.GET("/report", func(c *gin.Context) {
		handleGenerateReportRequest(c, ch)
	})

	// Start the server
	if err := router.Run(":8081"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
