package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func probeVideoCodecName(ctx context.Context, videoPath string) (string, error) {
	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "json",
		videoPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return videoCodecNameFromJSON(output)
}

func videoCodecNameFromJSON(output []byte) (string, error) {
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

func isHEVCCodec(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == "hevc" || codec == "h265" || codec == "h.265"
}

func appendCodecSpecificMP4Tag(ctx context.Context, args []string, fragments []string) []string {
	if len(fragments) == 0 {
		return args
	}
	codec, err := probeVideoCodecName(ctx, fragments[0])
	if err != nil {
		return args
	}
	if isHEVCCodec(codec) {
		args = append(args, "-tag:v", "hvc1")
	}
	return args
}
