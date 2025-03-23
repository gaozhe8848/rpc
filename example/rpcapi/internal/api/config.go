package api

// Config holds application configuration.
type Config struct {
	RabbitMQURL  string `validate:"required"`
	RequestQueue string `validate:"required"` // Queue for sending requests
	ServerPort   string `validate:"required"`
}
