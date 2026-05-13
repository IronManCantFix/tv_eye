package tvmonitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/r0n9/camkeep/constant"
)

type HAClient struct {
	config constant.TVMonitorConfig
	client *http.Client
}

func NewHAClient(cfg constant.TVMonitorConfig) *HAClient {
	return &HAClient{
		config: cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HAClient) TurnOffTV() error {
	return c.callService(c.config.HAService, map[string]interface{}{"entity_id": c.config.HAEntityID})
}

func (c *HAClient) SendTTS(message string) error {
	return c.callService("tts.google_translate_say", map[string]interface{}{
		"entity_id": c.config.HAEntityID,
		"message":   message,
	})
}

func (c *HAClient) callService(service string, body map[string]interface{}) error {
	parts := strings.SplitN(service, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid service format: %s (expected domain.service)", service)
	}

	url := fmt.Sprintf("%s/api/services/%s/%s", c.config.HAURL, parts[0], parts[1])
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("HA request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HA returned status %d", resp.StatusCode)
	}
	return nil
}

// TriggerShutdown sends TTS (if message configured), waits 5s, then turns off TV.
func (c *HAClient) TriggerShutdown(prefix, message string) {
	if message != "" {
		log.Printf("[%s] TTS: %q", prefix, message)
		if err := c.SendTTS(message); err != nil {
			log.Printf("[%s] TTS failed: %v", prefix, err)
		}
		time.Sleep(5 * time.Second)
	}

	log.Printf("[%s] Calling HA service %s for %s", prefix, c.config.HAService, c.config.HAEntityID)
	if err := c.TurnOffTV(); err != nil {
		log.Printf("[%s] Turn off failed: %v", prefix, err)
	}
}
