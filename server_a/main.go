package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// failOnError is a helper function to log an error and terminate the program.
func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %v", msg, err)
	}
}

// generateCorrelationID generates a unique string for identifying RPC requests.
func generateCorrelationID() string {
	rand.Seed(time.Now().UnixNano())
	return strconv.Itoa(rand.Int())
}

// handleReportRequest handles incoming RPC requests for financial reports.
func handleReportRequest(ch *amqp.Channel, delivery amqp.Delivery) {
	// 1. Unmarshal the request body.
	var request map[string]interface{}
	err := json.Unmarshal(delivery.Body, &request)
	if err != nil {
		log.Printf("Error unmarshalling request: %v", err)
		respondWithError(ch, delivery.ReplyTo, delivery.CorrelationId, "Invalid request format")
		return
	}

	// 2. Extract request parameters.
	requestID, ok := request["request_id"].(string)
	if !ok {
		log.Println("request_id missing or invalid")
		respondWithError(ch, delivery.ReplyTo, delivery.CorrelationId, "request_id missing or invalid")
		return
	}
	startDate, ok := request["start_date"].(string)
	if !ok {
		log.Println("start_date missing or invalid")
		respondWithError(ch, delivery.ReplyTo, delivery.CorrelationId, "start_date missing or invalid")
		return
	}
	endDate, ok := request["end_date"].(string)
	if !ok {
		log.Println("end_date missing or invalid")
		respondWithError(ch, delivery.ReplyTo, delivery.CorrelationId, "end_date missing or invalid")
		return
	}

	log.Printf("Received report request: request_id=%s, start_date=%s, end_date=%s\n", requestID, startDate, endDate)

	// 3. Send RPC requests to Server B and Server C.
	exchangeName := "financial_report_fanout"

	replyQueue, err := ch.QueueDeclare(
		"",
		false,
		true,
		true,
		false,
		nil,
	)
	failOnError(err, "Failed to declare a queue")

	correlationID := generateCorrelationID()

	reportRequest := map[string]interface{}{
		"request_id": requestID,
		"start_date": startDate,
		"end_date":   endDate,
	}
	reportRequestBody, err := json.Marshal(reportRequest)
	failOnError(err, "Failed to marshal report request")

	ctx := context.Background()

	err = ch.PublishWithContext(ctx,
		exchangeName,
		"",
		false,
		false,
		amqp.Publishing{
			ContentType:   "application/json",
			CorrelationId: correlationID,
			ReplyTo:       replyQueue.Name,
			Body:          reportRequestBody,
		})
	failOnError(err, "Failed to publish a message")

	// 4. Consume the responses from Server B and Server C.
	msgs, err := ch.Consume(
		replyQueue.Name,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to register a consumer")

	results := make(map[string]interface{})
	expectedServerIDs := map[string]bool{"server_b": true, "server_c": true}
	expectedResponses := len(expectedServerIDs)
	responsesReceived := 0

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		for msg := range msgs {
			if msg.CorrelationId == correlationID {
				var response map[string]interface{}
				err := json.Unmarshal(msg.Body, &response)
				failOnError(err, "Failed to unmarshal response")
				serverID := response["server_id"].(string)

				if _, ok := expectedServerIDs[serverID]; ok {
					results[serverID] = response["data"]
					delete(expectedServerIDs, serverID)
					responsesReceived++

					if responsesReceived >= expectedResponses {
						cancel()
						return
					}
				} else {
					log.Printf("Unexpected server ID: %s", serverID)
				}
			}
		}
	}()

	<-ctxTimeout.Done()

	// 5. Prepare and send the final response to the original client.
	if responsesReceived == expectedResponses {
		finalResponse := map[string]interface{}{
			"request_id": requestID,
			"status":     "success",
			"data":       results,
		}
		finalResponseBody, err := json.Marshal(finalResponse)
		failOnError(err, "Failed to marshal final response")

		err = ch.Publish(
			"",
			delivery.ReplyTo,
			false,
			false,
			amqp.Publishing{
				ContentType:   "application/json",
				CorrelationId: delivery.CorrelationId,
				Body:          finalResponseBody,
			},
		)
		failOnError(err, "Failed to publish final response")
		log.Printf("Sent response to client for request_id: %s\n", requestID)
	} else {
		errorMessage := fmt.Sprintf("Timeout or incomplete responses for request_id: %s. Missing responses from: %v", requestID, expectedServerIDs)
		log.Println(errorMessage)
		respondWithError(ch, delivery.ReplyTo, delivery.CorrelationId, errorMessage)
	}
}

// respondWithError sends an error response to the client.
func respondWithError(ch *amqp.Channel, replyTo, correlationID, errorMessage string) {
	errorResponse := map[string]interface{}{
		"status":  "error",
		"message": errorMessage,
	}
	errorResponseBody, err := json.Marshal(errorResponse)
	if err != nil {
		log.Printf("Error marshalling error response: %v", err)
		return
	}
	err = ch.Publish(
		"",
		replyTo,
		false,
		false,
		amqp.Publishing{
			ContentType:   "application/json",
			CorrelationId: correlationID,
			Body:          errorResponseBody,
		},
	)
	if err != nil {
		log.Printf("Error publishing error response: %v", err)
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

	// 3. Declare the queue for receiving client requests.
	rpcQueueName := "report_requests"
	_, err = ch.QueueDeclare(
		rpcQueueName,
		false,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to declare RPC queue")

	// 4. Consume requests from the RPC queue.
	msgs, err := ch.Consume(
		rpcQueueName,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to register consumer for RPC queue")

	fmt.Println("RPC server listening for report requests...")

	// 5. Process incoming RPC requests in a loop.
	for msg := range msgs {
		go handleReportRequest(ch, msg) // Handle each request in a new goroutine
	}
}
