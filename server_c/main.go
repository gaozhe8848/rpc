package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %v", msg, err)
	}
}

func main() {
	// 1. Establish a connection to RabbitMQ.
	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	failOnError(err, "Failed to connect to RabbitMQ")
	defer conn.Close()

	// 2. Open a channel.
	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	// 3. Declare the exchange.
	exchangeName := "financial_report_fanout"
	err = ch.ExchangeDeclare(
		exchangeName, // Name
		"fanout",     // Type
		true,         // Durable
		false,        // Auto-deleted
		false,        // Internal
		false,        // No-wait
		nil,          // Arguments
	)
	failOnError(err, "Failed to declare exchange")

	// 4. Declare the queue for this server.
	queueName := "server_c_queue"
	_, err = ch.QueueDeclare(
		queueName,
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to declare queue")

	// 5. Bind the queue to the fanout exchange.
	err = ch.QueueBind(
		queueName,
		"",
		exchangeName,
		false,
		nil,
	)
	failOnError(err, "Failed to bind queue")

	// 6. Consume messages from the queue.
	msgs, err := ch.Consume(
		queueName, // Queue name
		"",        // Consumer tag
		false,     // Auto-ack
		false,     // Exclusive
		false,     // No-local
		false,     // No-wait
		nil,       // Arguments
	)
	failOnError(err, "Failed to consume")

	// 7. Process messages in a loop.
	ctx := context.Background()
	for msg := range msgs {
		go func(msg amqp.Delivery) { // Pass the whole message to the goroutine
			// 8. Unmarshal the request body.
			var request map[string]interface{}
			err := json.Unmarshal(msg.Body, &request)
			if err != nil {
				log.Printf("Error unmarshalling request: %v", err)
				return
			}

			// 9. Simulate processing the request.
			time.Sleep(1 * time.Second)

			// 10. Prepare the response.
			data := map[string]interface{}{
				"db2_data": "data from database 2",
			}

			response := map[string]interface{}{
				"server_id": "server_c",
				"data":      data,
			}
			responseBody, err := json.Marshal(response)
			if err != nil {
				log.Printf("Error marshalling response: %v", err)
				return
			}

			// 11. Publish the response to the client's reply queue.
			err = ch.PublishWithContext(ctx,
				"",
				msg.ReplyTo, // Use the ReplyTo field from the message
				false,
				false,
				amqp.Publishing{
					ContentType:   "application/json",
					CorrelationId: msg.CorrelationId, // Use the CorrelationId from the message
					Body:          responseBody,
				},
			)
			if err != nil {
				log.Printf("Error publishing response: %v", err)
			}
		}(msg) // Pass the whole amqp.Delivery struct
	}
}
