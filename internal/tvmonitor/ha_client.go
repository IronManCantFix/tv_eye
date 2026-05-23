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

// PressIRButton 按下红外按钮 (如红外关机)。如果未配置则跳过。
func (c *HAClient) PressIRButton(prefix string) error {
	if c.config.HAIRTurnOffButtonID == "" {
		return fmt.Errorf("未配置红外关机按钮")
	}
	return c.callService("button.press", map[string]interface{}{"entity_id": c.config.HAIRTurnOffButtonID})
}

// PlayText 通过 HA 通知服务让音箱播放文本。
// 支持两种配置格式:
//   - "notify.send_message" → 直接作为服务调用，entity_id 需在 message body 中指定
//   - "notify.xiaomi_cn_xxx" → 识别为 notify 实体 ID，自动通过 notify.send_message 调用
func (c *HAClient) PlayText(prefix, message string) error {
	if c.config.HATTSService == "" {
		return fmt.Errorf("未配置音箱播放服务")
	}
	if message == "" {
		return fmt.Errorf("播放文本不能为空")
	}

	svc := c.config.HATTSService
	body := map[string]interface{}{"message": message}

	// 如果配置值是 "notify.xxx" 格式，视为 notify 实体 ID，用 notify.send_message 调用
	if strings.HasPrefix(svc, "notify.") && !strings.HasPrefix(svc, "notify.send_") {
		body["entity_id"] = svc
		svc = "notify.send_message"
	}

	return c.callService(svc, body)
}

// SendNotify 通过 HA 通知服务发送微信通知。如果未配置通知服务或开关已关闭则跳过。
func (c *HAClient) SendNotify(prefix, message string) {
	if !c.config.IsPhoneNotifyEnabled() {
		return
	}
	if c.config.HANotifyService == "" {
		return
	}
	body := map[string]interface{}{
		"wechat":  true,
		"message": message,
	}
	if err := c.callService(c.config.HANotifyService, body); err != nil {
		log.Printf("[%s] 发送微信通知失败: %v", prefix, err)
	} else {
		log.Printf("[%s] 微信通知已发送: %s", prefix, message)
	}
}

// TriggerShutdown 触发三种动作，彼此完全独立：
//   - 语音播报 (EnableVoiceNotify)
//   - 关闭电视 (EnableTVShutdown)
//   - 手机/微信提醒 (EnablePhoneNotify)，使用 notifyReason 作为通知内容
//
// 任一动作执行失败/跳过都不会影响另外两个。
func (c *HAClient) TriggerShutdown(prefix, ttsMessage, notifyReason string) {
	if c.config.IsVoiceNotifyEnabled() && c.config.HATTSService != "" && ttsMessage != "" {
		if err := c.PlayText(prefix, ttsMessage); err != nil {
			log.Printf("[%s] 播放提示失败: %v", prefix, err)
		} else {
			log.Printf("[%s] 播放提示成功", prefix)
			time.Sleep(5 * time.Second)
		}
	}
	if c.config.IsTVShutdownEnabled() && c.config.HAIRTurnOffButtonID != "" {
		if err := c.PressIRButton(prefix); err != nil {
			log.Printf("[%s] 红外关机失败: %v", prefix, err)
		} else {
			log.Printf("[%s] 红外关机成功", prefix)
		}
	}
	if notifyReason != "" {
		c.SendNotify(prefix, notifyReason)
	}
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
