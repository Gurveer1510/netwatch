package telemetry

import "time"

// Telemetry contains source-labeled percentages and the device observation time.
type Telemetry struct {
	Source        string    `json:"source"`
	DeviceID      string    `json:"device_id"`
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    float64   `json:"cpu_percent"`
	MemoryPercent float64   `json:"memory_percent"`
	PacketLossPct float64   `json:"packet_loss_pct"`
}
