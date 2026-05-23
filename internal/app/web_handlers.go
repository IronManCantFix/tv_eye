package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0n9/camkeep/constant"
	"github.com/r0n9/camkeep/internal/service"
	"github.com/r0n9/camkeep/internal/task"
	"github.com/r0n9/camkeep/internal/tvmonitor"
	"gopkg.in/yaml.v3"
)

type recordFile struct {
	Name string `json:"name"`
	Url  string `json:"url"`
	Size string `json:"size"` // 文件大小字符串
	Path string `json:"path"` // 相对路径，用于删除文件
}

type recordEntry struct {
	file    recordFile
	date    time.Time
	dateKey string
}

type recordDateRange struct {
	start    time.Time
	end      time.Time
	explicit bool
}

type probeResult struct {
	Codec     string `json:"codec"`
	IsH265    bool   `json:"is_h265"`
	CanProbe  bool   `json:"can_probe"`
	ProbeNote string `json:"probe_note,omitempty"`
}

const (
	recordDateLayout    = "2006-01-02"
	maxRecordRangeDays  = 7
	defaultRecordDayMax = 7
)

var recordDatePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func handleIndex(c *gin.Context) {
	constant.ConfigMux.RLock()
	tvMonitorEnabled := false
	var defaultTTSMessage string
	tvShutdownEnabled := true
	voiceNotifyEnabled := true
	phoneNotifyEnabled := true
	var maxSessionMinutes, maxDailyMinutes, restMinutes float64
	var actionGraceSec int
	for _, tm := range currentConfig.TVMonitors {
		if tm.Enabled {
			tvMonitorEnabled = true
			defaultTTSMessage = tm.HATTSMessage
			tvShutdownEnabled = tm.IsTVShutdownEnabled()
			voiceNotifyEnabled = tm.IsVoiceNotifyEnabled()
			phoneNotifyEnabled = tm.IsPhoneNotifyEnabled()
			maxSessionMinutes = tm.MaxSessionMinutes
			maxDailyMinutes = tm.MaxDailyMinutes
			restMinutes = tm.RestMinutes
			actionGraceSec = tm.ActionGraceSec
			break
		}
	}
	// 若没有启用的监控，从第一个监控配置读取值供 UI 预填充
	if !tvMonitorEnabled && len(currentConfig.TVMonitors) > 0 {
		tm := currentConfig.TVMonitors[0]
		maxSessionMinutes = tm.MaxSessionMinutes
		maxDailyMinutes = tm.MaxDailyMinutes
		restMinutes = tm.RestMinutes
		actionGraceSec = tm.ActionGraceSec
	}
	if actionGraceSec == 0 {
		actionGraceSec = 10
	}
	constant.ConfigMux.RUnlock()

	c.HTML(http.StatusOK, "index.html", gin.H{
		"Version":             version,
		"TVMonitorEnabled":    tvMonitorEnabled,
		"DefaultTTSMessage":   defaultTTSMessage,
		"TVShutdownEnabled":   tvShutdownEnabled,
		"VoiceNotifyEnabled":  voiceNotifyEnabled,
		"PhoneNotifyEnabled":  phoneNotifyEnabled,
		"MaxSessionMinutes":   maxSessionMinutes,
		"MaxDailyMinutes":     maxDailyMinutes,
		"RestMinutes":         restMinutes,
		"ActionGraceSec":      actionGraceSec,
	})
}

func handleStatus(c *gin.Context) {
	service.StatusMux.RLock()
	snapshot := make(map[string]gin.H, len(service.StatusMap))
	for id, status := range service.StatusMap {
		snapshot[id] = gin.H{
			"id":           status.ID,
			"is_running":   status.IsRunning,
			"record_state": status.RecordState,
			"start_time":   status.StartTime,
			"mode":         status.Mode,
			"record_time":  status.RecordTime,
			"stream_state": status.StreamState,
		}
	}
	service.StatusMux.RUnlock()

	for id, status := range snapshot {
		status["record_override"] = task.GetOverride(id)
	}
	c.JSON(200, snapshot)
}

func handleGetConfig(c *gin.Context) {
	data, _ := os.ReadFile(constant.ConfigFilePath) // 【修改】
	c.String(200, string(data))
}

func handleSaveConfig(c *gin.Context) {
	yamlBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "读取请求失败"})
		return
	}
	newConfig, err := parseConfigYAML(yamlBytes)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if err := os.WriteFile(constant.ConfigFilePath, yamlBytes, 0644); err != nil {
		c.JSON(500, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}

	// 异步重启任务，不阻塞前端请求
	go restartTasks(newConfig)
	c.JSON(200, gin.H{"msg": "配置已保存，系统正在热重启"})
}

func handleValidateConfig(c *gin.Context) {
	yamlBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "读取请求失败"})
		return
	}
	if _, err := parseConfigYAML(yamlBytes); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"msg": "配置格式检查通过"})
}

func handleCameraAction(c *gin.Context) {
	id := c.Param("id")
	action := c.Param("action") // start, stop, auto

	if !cameraExists(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "找不到该摄像头"})
		return
	}
	if err := task.SetOverride(id, action); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"msg": "指令已下发"})
}

func cameraExists(camID string) bool {
	constant.ConfigMux.RLock()
	defer constant.ConfigMux.RUnlock()
	return slices.ContainsFunc(currentConfig.Cameras, func(cam constant.Camera) bool {
		return cam.ID == camID
	})
}

func handleRecords(c *gin.Context) {
	camID := c.Param("id")
	dateRange, err := parseRecordDateRange(c.Query("start"), c.Query("end"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var entries []recordEntry
	baseDir := filepath.Join(constant.DefaultRecordBaseDir, camID)

	filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".mp4")) {
			relPath, _ := filepath.Rel(constant.DefaultRecordBaseDir, path)
			relPath = filepath.ToSlash(relPath)
			recordDate, ok := parseRecordDateFromPath(relPath)
			if !ok {
				return nil
			}

			// 2. 读取并格式化文件大小
			info, err := d.Info()
			if err != nil {
				return nil
			}
			sizeMB := float64(info.Size()) / (1024 * 1024)
			sizeStr := fmt.Sprintf("%.1f MB", sizeMB)

			entries = append(entries, recordEntry{
				date:    recordDate,
				dateKey: recordDate.Format(recordDateLayout),
				file: recordFile{
					Name: filepath.Base(path),
					Url:  "/play/" + relPath,
					Size: sizeStr,
					Path: relPath,
				},
			})
		}
		return nil
	})
	c.JSON(http.StatusOK, filterRecordEntries(entries, dateRange))
}

func parseRecordDateRange(startText, endText string) (recordDateRange, error) {
	startText = strings.TrimSpace(startText)
	endText = strings.TrimSpace(endText)
	if startText == "" && endText == "" {
		return recordDateRange{}, nil
	}
	if startText == "" || endText == "" {
		return recordDateRange{}, fmt.Errorf("开始日期和结束日期必须同时提供")
	}

	start, err := parseRecordDate(startText)
	if err != nil {
		return recordDateRange{}, fmt.Errorf("开始日期格式有误")
	}
	end, err := parseRecordDate(endText)
	if err != nil {
		return recordDateRange{}, fmt.Errorf("结束日期格式有误")
	}
	if end.Before(start) {
		return recordDateRange{}, fmt.Errorf("结束日期不能早于开始日期")
	}
	if recordDateSpanDays(start, end) > maxRecordRangeDays {
		return recordDateRange{}, fmt.Errorf("日期范围最多支持连续 %d 天", maxRecordRangeDays)
	}

	return recordDateRange{
		start:    start,
		end:      end,
		explicit: true,
	}, nil
}

func recordDateSpanDays(start, end time.Time) int {
	startUTC := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	return int(endUTC.Sub(startUTC)/(24*time.Hour)) + 1
}

func parseRecordDate(dateText string) (time.Time, error) {
	parsed, err := time.ParseInLocation(recordDateLayout, dateText, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.Format(recordDateLayout) != dateText {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return parsed, nil
}

func parseRecordDateFromPath(relPath string) (time.Time, bool) {
	pathParts := strings.Split(filepath.ToSlash(relPath), "/")
	for _, part := range pathParts[1:] {
		candidate := recordDatePattern.FindString(part)
		if candidate != part {
			continue
		}
		parsed, err := parseRecordDate(candidate)
		if err == nil {
			return parsed, true
		}
	}

	for _, candidate := range recordDatePattern.FindAllString(relPath, -1) {
		parsed, err := parseRecordDate(candidate)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func filterRecordEntries(entries []recordEntry, dateRange recordDateRange) []recordFile {
	sortRecordEntries(entries)

	var files []recordFile
	if dateRange.explicit {
		for _, entry := range entries {
			if entry.date.Before(dateRange.start) || entry.date.After(dateRange.end) {
				continue
			}
			files = append(files, entry.file)
		}
		return files
	}

	selectedDates := latestRecordDateKeys(entries, defaultRecordDayMax)
	for _, entry := range entries {
		if selectedDates[entry.dateKey] {
			files = append(files, entry.file)
		}
	}
	return files
}

func sortRecordEntries(entries []recordEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].date.Equal(entries[j].date) {
			return entries[i].date.After(entries[j].date)
		}
		return entries[i].file.Name > entries[j].file.Name
	})
}

func latestRecordDateKeys(entries []recordEntry, limit int) map[string]bool {
	dateSet := make(map[string]bool)
	for _, entry := range entries {
		dateSet[entry.dateKey] = true
	}

	dates := make([]string, 0, len(dateSet))
	for dateKey := range dateSet {
		dates = append(dates, dateKey)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))

	if len(dates) > limit {
		dates = dates[:limit]
	}

	selected := make(map[string]bool, len(dates))
	for _, dateKey := range dates {
		selected[dateKey] = true
	}
	return selected
}

func handleDeleteRecord(c *gin.Context) {
	fullPath, ok := safeRecordPath(c)
	if !ok {
		c.JSON(400, gin.H{"error": "非法的路径参数"})
		return
	}

	if err := os.Remove(fullPath); err != nil {
		c.JSON(500, gin.H{"error": "删除失败，文件可能已被清理"})
		return
	}

	c.JSON(200, gin.H{"msg": "录像删除成功"})
}

func handleDownloadRecord(c *gin.Context) {
	fullPath, ok := safeRecordPath(c)
	if !ok {
		c.JSON(400, gin.H{"error": "非法的路径参数"})
		return
	}

	if _, err := os.Stat(fullPath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "录像文件不存在"})
		return
	}

	c.FileAttachment(fullPath, filepath.Base(fullPath))
}

func handleProbeRecord(c *gin.Context) {
	fullPath, ok := safeRecordPath(c)
	if !ok {
		c.JSON(400, gin.H{"error": "非法的路径参数"})
		return
	}

	codec, err := probeVideoCodec(fullPath)
	if err != nil {
		c.JSON(http.StatusOK, probeResult{
			CanProbe:  false,
			ProbeNote: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, probeResult{
		Codec:    codec,
		IsH265:   isH265Codec(codec),
		CanProbe: true,
	})
}

func handleUnmanagedStreams(c *gin.Context) {
	go2rtcHost := fmt.Sprintf("http://%s:%d", constant.DefaultGo2rtcHost, constant.DefaultGo2rtcApiPort)
	resp, err := http.Get(go2rtcHost + "/api/streams")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法连接到 go2rtc"})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解析 go2rtc 响应失败"})
		return
	}

	var streamKeys []string
	if streamsObj, ok := result["streams"].(map[string]interface{}); ok {
		for k := range streamsObj {
			streamKeys = append(streamKeys, k)
		}
	} else {
		for k := range result {
			streamKeys = append(streamKeys, k)
		}
	}

	// 过滤掉已经在 conf.yaml 中被 CamKeep 管理的流
	constant.ConfigMux.RLock()
	managed := make(map[string]bool)
	for _, cam := range currentConfig.Cameras {
		managed[cam.ID] = true
	}
	constant.ConfigMux.RUnlock()

	var unmanaged []string
	for _, k := range streamKeys {
		if !managed[k] {
			unmanaged = append(unmanaged, k)
		}
	}

	c.JSON(http.StatusOK, unmanaged)
}

func handleGo2rtcConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"host": constant.DefaultGo2rtcHost,
		"port": constant.DefaultGo2rtcApiPort,
	})
}

func handlePlayHLS(c *gin.Context) {
	tsPath := c.Param("filepath") // 获取路径，例如: /front-door/2026-04-27/12-00-00.ts
	if !strings.HasSuffix(tsPath, ".ts") {
		c.String(400, "仅支持 ts 格式转换为 HLS")
		return
	}

	// 构造一个只包含单一文件的虚拟 M3U8 列表，欺骗 iOS 原生播放器
	m3u8Content := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3600\n#EXTINF:3600.0,\n/play%s\n#EXT-X-ENDLIST\n", tsPath)

	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.Header("Cache-Control", "no-cache")
	c.String(200, m3u8Content)
}

// handlePlayRemux 实时重封装：零损耗、零转码、极低CPU，用于浏览器直接硬解 H.265
func handlePlayRemux(c *gin.Context) {
	fullPath, ok := safeRecordPathFromParam(c.Param("filepath"))
	if !ok {
		c.String(http.StatusBadRequest, "非法的路径参数")
		return
	}

	// 核心魔法参数：-c:v copy 彻底跳过视频转码
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-i", fullPath,
		"-map", "0:v:0", // 显式映射第一个视频流
		"-map", "0:a?", // 显式映射音频流（如果有的话）
		"-c:v", "copy", // 直接复制 H.265 原始数据
		"-tag:v", "hvc1", // 强制将 HEVC 标签设为 hvc1，满足苹果 Safari 的苛刻要求
		"-c:a", "aac", // 音频由于监控多为 G711，浏览器不支持，需要转码 AAC（极低开销）
		"-f", "mp4", // 封装为 MP4
		"-movflags", "frag_keyframe+empty_moov", // 让 MP4 变成流式结构 (Fragmented MP4)，不需要等文件全部处理完就能播放
		"pipe:1", // 输出到标准流
	}

	cmd := exec.CommandContext(c.Request.Context(), "ffmpeg", args...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.String(http.StatusInternalServerError, "重封装初始化失败")
		return
	}
	if err := cmd.Start(); err != nil {
		c.String(http.StatusInternalServerError, "重封装启动失败")
		return
	}

	// 告诉浏览器这直接就是一个 MP4 视频流
	c.Header("Content-Type", "video/mp4")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	// 将 fMP4 数据流直接打给前端
	c.DataFromReader(http.StatusOK, -1, "video/mp4", stdout, nil)

	if err := cmd.Wait(); err != nil && c.Request.Context().Err() == nil {
		log.Printf("实时重封装进程退出异常: %v", err)
	}
}

func handlePlayTranscode(c *gin.Context) {
	fullPath, ok := safeRecordPathFromParam(c.Param("filepath"))
	if !ok {
		c.String(http.StatusBadRequest, "非法的路径参数")
		return
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-i", fullPath,
		"-map", "0:v:0",
		"-map", "0:a?",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-f", "mpegts",
		"pipe:1",
	}

	cmd := exec.CommandContext(c.Request.Context(), "ffmpeg", args...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.String(http.StatusInternalServerError, "转码初始化失败")
		return
	}
	if err := cmd.Start(); err != nil {
		c.String(http.StatusInternalServerError, "转码启动失败")
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.DataFromReader(http.StatusOK, -1, "video/mp2t", stdout, nil)

	if err := cmd.Wait(); err != nil && c.Request.Context().Err() == nil {
		log.Printf("按需转码进程退出异常: %v", err)
	}
}

func handleWebRTCProxy(c *gin.Context) {
	camID := c.Param("id")

	constant.ConfigMux.RLock()
	var targetCam *constant.Camera
	for _, cam := range currentConfig.Cameras {
		if cam.ID == camID {
			targetCam = &cam
			break
		}
	}
	constant.ConfigMux.RUnlock()

	if targetCam == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "找不到该摄像头"})
		return
	}

	// 读取前端发来的 WebRTC SDP Offer
	sdpOffer, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取 SDP offer"})
		return
	}

	go2rtcHost := fmt.Sprintf("http://%s:%d", constant.DefaultGo2rtcHost, constant.DefaultGo2rtcApiPort)

	// 接口被调用时不再需要发送 PUT 注册流，因为启动时已经统一注册好了！
	// 直接发起 WebRTC 握手：
	go2rtcWebRTCURL := fmt.Sprintf("%s/api/webrtc?src=%s", go2rtcHost, camID)

	resp, err := http.Post(go2rtcWebRTCURL, "application/sdp", bytes.NewReader(sdpOffer))
	if err != nil {
		log.Printf("[%s] 连接 go2rtc 失败: %v", camID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "视频流网关连接失败"})
		return
	}
	defer resp.Body.Close()

	// 判断包含 200 和 201 状态码 (创建成功)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("[%s] go2rtc 拒绝了 WebRTC 请求，状态码: %d, 返回内容: %s", camID, resp.StatusCode, string(bodyBytes))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "视频网关握手拒绝"})
		return
	}

	// 将 go2rtc 返回的 SDP Answer 原封不动返回给前端
	c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

func safeRecordPath(c *gin.Context) (string, bool) {
	return safeRecordPathFromParam(c.Query("path"))
}

func safeRecordPathFromParam(targetPath string) (string, bool) {
	targetPath = strings.TrimPrefix(targetPath, "/")
	if targetPath == "" || strings.Contains(targetPath, "..") {
		return "", false
	}
	if !strings.HasSuffix(targetPath, ".ts") && !strings.HasSuffix(targetPath, ".mp4") {
		return "", false
	}
	return filepath.Join(constant.DefaultRecordBaseDir, targetPath), true
}

func probeVideoCodec(fullPath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "json",
		fullPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var result struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", err
	}
	if len(result.Streams) == 0 {
		return "", fmt.Errorf("未找到视频流")
	}
	return result.Streams[0].CodecName, nil
}

func isH265Codec(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == "hevc" || codec == "h265" || codec == "h.265"
}

func handleTVMonitorStatus(c *gin.Context) {
	statuses := tvmonitor.GetAllStatuses()
	c.JSON(http.StatusOK, statuses)
}

func handleTVMonitorLogs(c *gin.Context) {
	logs := tvmonitor.GetRecentLogs(50)
	c.JSON(http.StatusOK, logs)
}

func handleTVMonitorSnapshot(c *gin.Context) {
	cameraID := c.Param("id")

	// Return the cached snapshot and request a fresh one for next time
	jpeg, ok := tvmonitor.GetSnapshot(cameraID)
	tvmonitor.RequestSnapshot(cameraID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "暂无快照"})
		return
	}
	c.Header("Content-Type", "image/jpeg")
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "image/jpeg", jpeg)
}

func handleTVMonitorClearLogs(c *gin.Context) {
	tvmonitor.ClearLogs()
	c.JSON(http.StatusOK, gin.H{"msg": "日志已清除"})
}

func handleTVMonitorIRTurnOff(c *gin.Context) {
	cfg := getFirstEnabledTVMonitor()
	if cfg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到启用的电视监控配置"})
		return
	}
	ha := tvmonitor.NewHAClient(*cfg)
	if err := ha.PressIRButton("ir_turn_off"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"msg": "红外关机指令已发送"})
}

func handleTVMonitorPlayText(c *gin.Context) {
	var req struct {
		Message string `json:"message"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "播放文本不能为空"})
		return
	}

	cfg := getFirstEnabledTVMonitor()
	if cfg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到启用的电视监控配置"})
		return
	}
	ha := tvmonitor.NewHAClient(*cfg)
	if err := ha.PlayText("play_text", req.Message); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"msg": "播放文本已发送"})
}

func handleTVMonitorToggle(c *gin.Context) {
	data, err := os.ReadFile(constant.ConfigFilePath)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取配置失败"})
		return
	}

	newConfig, err := parseConfigYAML(data)
	if err != nil {
		c.JSON(400, gin.H{"error": "解析配置失败: " + err.Error()})
		return
	}

	if len(newConfig.TVMonitors) == 0 {
		c.JSON(400, gin.H{"error": "未配置 tv_monitors"})
		return
	}

	// Toggle enabled state
	newConfig.TVMonitors[0].Enabled = !newConfig.TVMonitors[0].Enabled
	newEnabled := newConfig.TVMonitors[0].Enabled

	// Marshal back to YAML and save
	out, err := yaml.Marshal(newConfig)
	if err != nil {
		c.JSON(500, gin.H{"error": "序列化配置失败"})
		return
	}

	// Preserve the header comment if present
	header := ""
	content := string(data)
	if idx := strings.Index(content, "---\n"); idx >= 0 {
		header = content[:idx+4]
	} else if strings.HasPrefix(content, "#") {
		if nlIdx := strings.Index(content, "\n"); nlIdx >= 0 {
			header = content[:nlIdx+1] + "\n"
		}
	}

	finalOut := []byte(header + string(out))
	if err := os.WriteFile(constant.ConfigFilePath, finalOut, 0644); err != nil {
		c.JSON(500, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}

	go restartTasks(newConfig)
	c.JSON(200, gin.H{"enabled": newEnabled})
}

// handleTVMonitorActionToggle 切换电视监控触发的三个动作开关之一:
// action: "tv_shutdown" / "voice_notify" / "phone_notify"
func handleTVMonitorActionToggle(c *gin.Context) {
	action := c.Param("action")
	if action != "tv_shutdown" && action != "voice_notify" && action != "phone_notify" {
		c.JSON(400, gin.H{"error": "未知动作: " + action})
		return
	}

	data, err := os.ReadFile(constant.ConfigFilePath)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取配置失败"})
		return
	}

	newConfig, err := parseConfigYAML(data)
	if err != nil {
		c.JSON(400, gin.H{"error": "解析配置失败: " + err.Error()})
		return
	}

	if len(newConfig.TVMonitors) == 0 {
		c.JSON(400, gin.H{"error": "未配置 tv_monitors"})
		return
	}

	tm := &newConfig.TVMonitors[0]
	var newEnabled bool
	switch action {
	case "tv_shutdown":
		newEnabled = !tm.IsTVShutdownEnabled()
		tm.EnableTVShutdown = &newEnabled
	case "voice_notify":
		newEnabled = !tm.IsVoiceNotifyEnabled()
		tm.EnableVoiceNotify = &newEnabled
	case "phone_notify":
		newEnabled = !tm.IsPhoneNotifyEnabled()
		tm.EnablePhoneNotify = &newEnabled
	}

	out, err := yaml.Marshal(newConfig)
	if err != nil {
		c.JSON(500, gin.H{"error": "序列化配置失败"})
		return
	}

	header := ""
	content := string(data)
	if idx := strings.Index(content, "---\n"); idx >= 0 {
		header = content[:idx+4]
	} else if strings.HasPrefix(content, "#") {
		if nlIdx := strings.Index(content, "\n"); nlIdx >= 0 {
			header = content[:nlIdx+1] + "\n"
		}
	}

	finalOut := []byte(header + string(out))
	if err := os.WriteFile(constant.ConfigFilePath, finalOut, 0644); err != nil {
		c.JSON(500, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}

	go restartTasks(newConfig)
	c.JSON(200, gin.H{"action": action, "enabled": newEnabled})
}

// handleTVMonitorUpdateLimits 更新单次时长、每日总时长、休息冷却、动作保护期四个参数。
func handleTVMonitorUpdateLimits(c *gin.Context) {
	var req struct {
		MaxSessionMinutes *float64 `json:"max_session_minutes"`
		MaxDailyMinutes   *float64 `json:"max_daily_minutes"`
		RestMinutes       *float64 `json:"rest_minutes"`
		ActionGraceSec    *int     `json:"action_grace_sec"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}

	check := func(name string, v *float64) error {
		if v == nil {
			return nil
		}
		if *v <= 0 || *v > 1440 {
			return fmt.Errorf("%s 必须在 (0, 1440] 区间内", name)
		}
		return nil
	}
	for _, e := range []error{
		check("max_session_minutes", req.MaxSessionMinutes),
		check("max_daily_minutes", req.MaxDailyMinutes),
		check("rest_minutes", req.RestMinutes),
	} {
		if e != nil {
			c.JSON(400, gin.H{"error": e.Error()})
			return
		}
	}
	if req.ActionGraceSec != nil && (*req.ActionGraceSec < 10 || *req.ActionGraceSec > 600) {
		c.JSON(400, gin.H{"error": "action_grace_sec 必须在 [10, 600] 区间内"})
		return
	}

	data, err := os.ReadFile(constant.ConfigFilePath)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取配置失败"})
		return
	}

	newConfig, err := parseConfigYAML(data)
	if err != nil {
		c.JSON(400, gin.H{"error": "解析配置失败: " + err.Error()})
		return
	}

	if len(newConfig.TVMonitors) == 0 {
		c.JSON(400, gin.H{"error": "未配置 tv_monitors"})
		return
	}

	tm := &newConfig.TVMonitors[0]
	if req.MaxSessionMinutes != nil {
		tm.MaxSessionMinutes = *req.MaxSessionMinutes
	}
	if req.MaxDailyMinutes != nil {
		tm.MaxDailyMinutes = *req.MaxDailyMinutes
	}
	if req.RestMinutes != nil {
		tm.RestMinutes = *req.RestMinutes
	}
	if req.ActionGraceSec != nil {
		tm.ActionGraceSec = *req.ActionGraceSec
	}

	out, err := yaml.Marshal(newConfig)
	if err != nil {
		c.JSON(500, gin.H{"error": "序列化配置失败"})
		return
	}

	header := ""
	content := string(data)
	if idx := strings.Index(content, "---\n"); idx >= 0 {
		header = content[:idx+4]
	} else if strings.HasPrefix(content, "#") {
		if nlIdx := strings.Index(content, "\n"); nlIdx >= 0 {
			header = content[:nlIdx+1] + "\n"
		}
	}

	finalOut := []byte(header + string(out))
	if err := os.WriteFile(constant.ConfigFilePath, finalOut, 0644); err != nil {
		c.JSON(500, gin.H{"error": "保存配置失败: " + err.Error()})
		return
	}

	go restartTasks(newConfig)
	c.JSON(200, gin.H{
		"max_session_minutes": tm.MaxSessionMinutes,
		"max_daily_minutes":   tm.MaxDailyMinutes,
		"rest_minutes":        tm.RestMinutes,
		"action_grace_sec":    tm.ActionGraceSec,
	})
}

func getFirstEnabledTVMonitor() *constant.TVMonitorConfig {
	constant.ConfigMux.RLock()
	defer constant.ConfigMux.RUnlock()
	for _, tm := range currentConfig.TVMonitors {
		if tm.Enabled {
			return &tm
		}
	}
	return nil
}
