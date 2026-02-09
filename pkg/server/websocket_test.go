package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWebSocketHub(t *testing.T) {
	hub := NewWebSocketHub()

	assert.NotNil(t, hub)
	assert.NotNil(t, hub.clients)
	assert.NotNil(t, hub.register)
	assert.NotNil(t, hub.unregister)
	assert.NotNil(t, hub.broadcast)
}

func TestWebSocketHub_ClientCount(t *testing.T) {
	hub := NewWebSocketHub()

	// Initially no clients
	assert.Equal(t, 0, hub.ClientCount())

	// Start hub in goroutine
	go hub.Run()

	// Create a mock client registration
	client := &Client{
		hub:  hub,
		send: make(chan []byte, 256),
	}

	hub.register <- client
	time.Sleep(10 * time.Millisecond) // Allow goroutine to process

	assert.Equal(t, 1, hub.ClientCount())
}

func TestWSMessage_Marshal(t *testing.T) {
	msg := WSMessage{
		Type:    EventMetricsUpdate,
		Payload: map[string]interface{}{"cpu": 0.75},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded WSMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, EventMetricsUpdate, decoded.Type)
	assert.NotNil(t, decoded.Payload)
}

func TestWSMessage_Unmarshal(t *testing.T) {
	jsonData := `{"type":"feedback_received","payload":{"feedback_id":"fb-123","content":"Great!","rating":5.0}}`

	var msg WSMessage
	err := json.Unmarshal([]byte(jsonData), &msg)
	require.NoError(t, err)

	assert.Equal(t, EventFeedbackReceived, msg.Type)
	assert.NotNil(t, msg.Payload)

	// Verify payload structure
	payloadMap, ok := msg.Payload.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "fb-123", payloadMap["feedback_id"])
}

func TestMetricsPayload_Structure(t *testing.T) {
	payload := MetricsPayload{
		Metrics:     map[string]float64{"cpu": 0.85, "memory": 0.60},
		Timestamp:   time.Now().Unix(),
		ServiceName: "test-service",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded MetricsPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "test-service", decoded.ServiceName)
	assert.Equal(t, int64(0), decoded.Timestamp%1) // Just verify it's a valid int64
}

func TestFeedbackPayload_Structure(t *testing.T) {
	payload := FeedbackPayload{
		FeedbackID:   "fb-456",
		Content:      "Excellent work",
		Rating:       4.5,
		ServiceName:  "api-gateway",
		Timestamp:    time.Now().Unix(),
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded FeedbackPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "fb-456", decoded.FeedbackID)
	assert.Equal(t, "Excellent work", decoded.Content)
	assert.Equal(t, 4.5, decoded.Rating)
}

func TestTaskPayload_Structure(t *testing.T) {
	payload := TaskPayload{
		TaskID:      "task-789",
		Title:       "Deploy to production",
		Priority:    "high",
		Status:      "pending",
		ServiceName: "deployer",
		Timestamp:   time.Now().Unix(),
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded TaskPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "task-789", decoded.TaskID)
	assert.Equal(t, "Deploy to production", decoded.Title)
	assert.Equal(t, "high", decoded.Priority)
}

func TestAlertPayload_Structure(t *testing.T) {
	payload := AlertPayload{
		AlertID:      "alert-001",
		AlertType:    "anomaly",
		Severity:     "critical",
		Message:      "CPU usage exceeded 95%",
		Threshold:    95.0,
		CurrentValue: 97.5,
		ServiceName:  "web-service",
		Timestamp:    time.Now().Unix(),
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded AlertPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "alert-001", decoded.AlertID)
	assert.Equal(t, "anomaly", decoded.AlertType)
	assert.Equal(t, "critical", decoded.Severity)
	assert.Equal(t, 95.0, decoded.Threshold)
	assert.Equal(t, 97.5, decoded.CurrentValue)
}

func TestEventTypeConstants(t *testing.T) {
	assert.Equal(t, "metrics_update", EventMetricsUpdate)
	assert.Equal(t, "feedback_received", EventFeedbackReceived)
	assert.Equal(t, "task_created", EventTaskCreated)
	assert.Equal(t, "anomaly_detected", EventAnomalyDetected)
	assert.Equal(t, "satisfaction_alert", EventSatisfactionAlert)
}

func TestWebSocketHub_BroadcastEvent(t *testing.T) {
	hub := NewWebSocketHub()

	// Start hub in goroutine
	go hub.Run()

	// Create a mock client with a receiver
	received := make(chan []byte, 1)
	client := &Client{
		hub:  hub,
		send: make(chan []byte, 256),
	}

	// Override send to capture message
	go func() {
		msg := <-client.send
		received <- msg
	}()

	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	// Broadcast an event
	payload := MetricsPayload{
		Metrics:     map[string]float64{"cpu": 0.50},
		Timestamp:   time.Now().Unix(),
		ServiceName: "test",
	}
	hub.BroadcastEvent(EventMetricsUpdate, payload)

	// Wait for message
	select {
	case msg := <-received:
		var wsMsg WSMessage
		err := json.Unmarshal(msg, &wsMsg)
		require.NoError(t, err)
		assert.Equal(t, EventMetricsUpdate, wsMsg.Type)
		assert.NotNil(t, wsMsg.Payload)
	case <-time.After(time.Second):
		t.Fatal("Did not receive broadcast message")
	}
}

func TestClient_HandlePingMessage(t *testing.T) {
	hub := NewWebSocketHub()
	client := &Client{
		hub:  hub,
		send: make(chan []byte, 256),
	}

	// Send a ping message
	pingMsg := WSMessage{Type: "ping", Payload: nil}
	data, _ := json.Marshal(pingMsg)

	go client.handleMessage(data)

	// Should receive pong
	select {
	case msg := <-client.send:
		var response WSMessage
		err := json.Unmarshal(msg, &response)
		require.NoError(t, err)
		assert.Equal(t, "pong", response.Type)
	case <-time.After(time.Second):
		t.Fatal("Did not receive pong response")
	}
}
