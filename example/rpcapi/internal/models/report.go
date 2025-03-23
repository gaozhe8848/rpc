package models

// ReportRequest struct for the request body
type ReportRequest struct {
	ReportID string `json:"report_id" validate:"required"`
	// Add other request parameters as needed
}
