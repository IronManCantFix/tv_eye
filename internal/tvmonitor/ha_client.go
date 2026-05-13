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

// ExecuteControl 执行遥控器控制动作。如果未配置 ha_control_service 则跳过。
func (c *HAClient) ExecuteControl(prefix string) {
	if c.config.HAControlService == "" || c.config.HAControlEntityID == "" {
		log.Printf("[%s] 未配置遥控器控制，跳过", prefix)
		return
	}
	log.Printf("[%s] 正在调用 HA 控制服务 %s, 实体: %s", prefix, c.config.HAControlService, c.config.HAControlEntityID)
	if err := c.callService(c.config.HAControlService, map[string]interface{}{"entity_id": c.config.HAControlEntityID}); err != nil {
		log.Printf("[%s] 遥控器控制失败: %v", prefix, err)
	} else {
		log.Printf("[%s] 遥控器控制成功", prefix)
	}
}

// ExecuteTTS 执行 TTS 语音播报。如果未配置 ha_tts_entity_id 或 message 为空则跳过。
func (c *HAClient) ExecuteTTS(prefix, message string) {
	if c.config.HATTSEntityID == "" {
		log.Printf("[%s] 未配置 TTS 实体，跳过语音播报", prefix)
		return
	}
	if message == "" {
		log.Printf("[%s] TTS 消息为空，跳过语音播报", prefix)
		return
	}
	log.Printf("[%s] 正在发送 TTS 播报到 %s: %q", prefix, c.config.HATTSEntityID, message)
	if err := c.callService("tts.google_translate_say", map[string]interface{}{
		"entity_id": c.config.HATTSEntityID,
		"message":   message,
	}); err != nil {
		log.Printf("[%s] TTS 播报失败: %v", prefix, err)
	} else {
		log.Printf("[%s] TTS 播报成功", prefix)
	}
}

// TriggerShutdown 先播报 TTS (如有)，等待 5 秒，再执行遥控器控制 (如有)。
func (c *HAClient) TriggerShutdown(prefix, ttsMessage string) {
	c.ExecuteTTS(prefix, ttsMessage)
	if c.config.HATTSEntityID != "" && ttsMessage != "" {
		log.Printf("[%s] 等待 5 秒让 TTS 播放完毕...", prefix)
		time.Sleep(5 * time.Second)
	}
	c.ExecuteControl(prefix)
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
