package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// EventType represents the type of notification event
type EventType string

const (
	EventContainerUpdated   EventType = "container_updated"
	EventContainerRestarted EventType = "container_restarted"
	EventContainerUnhealthy EventType = "container_unhealthy"
	EventContainerGaveUp    EventType = "container_gave_up"
	EventUpdateCycleStart   EventType = "update_cycle_start"
	EventUpdateCycleEnd     EventType = "update_cycle_end"
)

// Event represents a notification event
type Event struct {
	Type          EventType              `json:"type"`
	ContainerID   string                 `json:"container_id,omitempty"`
	ContainerName string                 `json:"container_name,omitempty"`
	Image         string                 `json:"image,omitempty"`
	Message       string                 `json:"message"`
	Timestamp     time.Time              `json:"timestamp"`
	Extra         map[string]interface{} `json:"extra,omitempty"`
}

// Notifier handles sending notifications
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// New creates a new Notifier
func New(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send sends a notification event
func (n *Notifier) Send(event Event) error {
	if n.webhookURL == "" {
		log.Debugf("No notification URL configured, skipping notification")
		return nil
	}

	event.Timestamp = time.Now()

	// Detect webhook type and format accordingly
	if strings.Contains(n.webhookURL, "discord.com/api/webhooks") {
		return n.sendDiscord(event)
	} else if strings.Contains(n.webhookURL, "hooks.slack.com") {
		return n.sendSlack(event)
	} else {
		return n.sendGeneric(event)
	}
}

// sendDiscord sends a Discord webhook notification
func (n *Notifier) sendDiscord(event Event) error {
	color := 0x3498db // Blue default

	switch event.Type {
	case EventContainerUpdated:
		color = 0x2ecc71 // Green
	case EventContainerUnhealthy, EventContainerGaveUp:
		color = 0xe74c3c // Red
	case EventContainerRestarted:
		color = 0xf39c12 // Orange
	}

	fields := []map[string]interface{}{}
	if event.ContainerName != "" {
		fields = append(fields, map[string]interface{}{
			"name":   "Container",
			"value":  event.ContainerName,
			"inline": true,
		})
	}
	if event.Image != "" {
		fields = append(fields, map[string]interface{}{
			"name":   "Image",
			"value":  fmt.Sprintf("`%s`", event.Image),
			"inline": true,
		})
	}

	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       fmt.Sprintf("🐳 DockWarden: %s", event.Type),
				"description": event.Message,
				"color":       color,
				"fields":      fields,
				"timestamp":   event.Timestamp.Format(time.RFC3339),
				"footer": map[string]string{
					"text": "DockWarden",
				},
			},
		},
	}

	return n.post(payload)
}

// sendSlack sends a Slack webhook notification
func (n *Notifier) sendSlack(event Event) error {
	emoji := ":whale:"
	switch event.Type {
	case EventContainerUpdated:
		emoji = ":white_check_mark:"
	case EventContainerUnhealthy, EventContainerGaveUp:
		emoji = ":x:"
	case EventContainerRestarted:
		emoji = ":arrows_counterclockwise:"
	}

	text := fmt.Sprintf("%s *DockWarden:* %s", emoji, event.Message)
	if event.ContainerName != "" {
		text += fmt.Sprintf("\n• Container: `%s`", event.ContainerName)
	}
	if event.Image != "" {
		text += fmt.Sprintf("\n• Image: `%s`", event.Image)
	}

	payload := map[string]interface{}{
		"text": text,
	}

	return n.post(payload)
}

// sendGeneric sends a generic JSON webhook notification
func (n *Notifier) sendGeneric(event Event) error {
	payload := map[string]interface{}{
		"source":    "dockwarden",
		"type":      event.Type,
		"message":   event.Message,
		"timestamp": event.Timestamp.Format(time.RFC3339),
	}

	if event.ContainerName != "" {
		payload["container_name"] = event.ContainerName
		payload["container_id"] = event.ContainerID
	}
	if event.Image != "" {
		payload["image"] = event.Image
	}
	if event.Extra != nil {
		for k, v := range event.Extra {
			payload[k] = v
		}
	}

	return n.post(payload)
}

// post sends a POST request with JSON payload
func (n *Notifier) post(payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal notification payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create notification request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DockWarden/1.0")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notification webhook returned status %d", resp.StatusCode)
	}

	log.Debugf("Notification sent successfully to %s", n.webhookURL)
	return nil
}

// NotifyContainerUpdated sends a container updated notification
func (n *Notifier) NotifyContainerUpdated(containerName, image, oldDigest, newDigest string) {
	event := Event{
		Type:          EventContainerUpdated,
		ContainerName: containerName,
		Image:         image,
		Message:       fmt.Sprintf("Container %s has been updated", containerName),
		Extra: map[string]interface{}{
			"old_digest": oldDigest,
			"new_digest": newDigest,
		},
	}
	if err := n.Send(event); err != nil {
		log.Warnf("Failed to send notification: %v", err)
	}
}

// NotifyContainerUnhealthy sends an unhealthy container notification
func (n *Notifier) NotifyContainerUnhealthy(containerName, image string, attempts int) {
	event := Event{
		Type:          EventContainerUnhealthy,
		ContainerName: containerName,
		Image:         image,
		Message:       fmt.Sprintf("Container %s is unhealthy (attempt %d)", containerName, attempts),
		Extra: map[string]interface{}{
			"restart_attempts": attempts,
		},
	}
	if err := n.Send(event); err != nil {
		log.Warnf("Failed to send notification: %v", err)
	}
}

// NotifyContainerGaveUp sends a gave up notification
func (n *Notifier) NotifyContainerGaveUp(containerName, image string, maxAttempts int) {
	event := Event{
		Type:          EventContainerGaveUp,
		ContainerName: containerName,
		Image:         image,
		Message:       fmt.Sprintf("Container %s: giving up after %d restart attempts. Waiting for new image version.", containerName, maxAttempts),
		Extra: map[string]interface{}{
			"max_attempts": maxAttempts,
		},
	}
	if err := n.Send(event); err != nil {
		log.Warnf("Failed to send notification: %v", err)
	}
}
