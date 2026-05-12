package task

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r0n9/camkeep/constant"
)

const mergedSuffix = "_merged"

// DailyMergeTask 每天定时把前一天的碎片录像合并为单文件。
func DailyMergeTask(ctx context.Context, wg *sync.WaitGroup, cfg constant.Config) {
	defer wg.Done()

	if !cfg.DailyMerge.Enabled {
		return
	}

	for {
		nextRun, err := nextDailyMergeRun(time.Now(), cfg.DailyMerge.Time)
		if err != nil {
			log.Printf("每日录像合并任务配置无效，已跳过: %v", err)
			return
		}

		timer := time.NewTimer(time.Until(nextRun))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			mergeDate := nextRun.AddDate(0, 0, -1).Format("2006-01-02")
			log.Printf("开始执行每日录像合并任务，目标日期: %s", mergeDate)
			for _, cam := range cfg.Cameras {
				if err := mergeCameraDate(ctx, cam, mergeDate); err != nil {
					log.Printf("[%s] 合并 %s 录像失败: %v", cam.ID, mergeDate, err)
				}
			}
		}
	}
}

func nextDailyMergeRun(now time.Time, timeStr string) (time.Time, error) {
	parts := strings.Split(timeStr, ":")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("daily_merge.time 必须使用 HH:mm 格式")
	}

	runClock, err := time.ParseInLocation("15:04", timeStr, now.Location())
	if err != nil {
		return time.Time{}, err
	}

	next := time.Date(now.Year(), now.Month(), now.Day(), runClock.Hour(), runClock.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

func mergeCameraDate(ctx context.Context, cam constant.Camera, date string) error {
	if cam.ID == "" || cam.Format == "" {
		return nil
	}

	dateDir := filepath.Join(constant.DefaultRecordBaseDir, cam.ID, date)
	entries, err := os.ReadDir(dateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// 原始碎片的扩展名 (例如 .ts)
	ext := "." + strings.TrimPrefix(cam.Format, ".")

	// 强制最终合并文件的扩展名为 .mp4
	mergedExt := ".mp4"
	mergedName := fmt.Sprintf("%s_%s%s%s", cam.ID, date, mergedSuffix, mergedExt)
	mergedPath := filepath.Join(dateDir, mergedName)
	if _, err := os.Stat(mergedPath); err == nil {
		return nil
	}

	var fragments []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 找出当天所有的原始碎片，且排除已存在的合并文件
		if filepath.Ext(name) != ext || strings.Contains(name, mergedSuffix) {
			continue
		}
		fragments = append(fragments, filepath.Join(dateDir, name))
	}

	if len(fragments) < 2 {
		return nil
	}
	sort.Strings(fragments)

	// 临时文件也强制使用 mp4 后缀
	tempOutput := mergedPath + ".tmp" + mergedExt
	listPath, err := writeConcatList(fragments)
	if err != nil {
		return err
	}
	defer os.Remove(listPath)
	defer os.Remove(tempOutput)

	// FFmpeg 参数，打造纯净完美的 Web 播放格式
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y", // 强制覆盖可能存在的异常临时文件
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c:v", "copy", // 视频无损极速拼接 (占用极低 CPU)
		"-c:a", "aac", // 监控音频多为 alaw/ulaw(G.711)，必须转码为 AAC，否则浏览器没声音
		"-movflags", "+faststart", // 将 moov atom 移到文件头部，完美支持超大文件的 HTTP Range 拖拽秒播
	}
	args = appendCodecSpecificMP4Tag(ctx, args, fragments)
	args = append(args, tempOutput)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 合并失败: %v, output=%s", err, strings.TrimSpace(string(output)))
	}

	if err := os.Rename(tempOutput, mergedPath); err != nil {
		return err
	}

	// 合并成功后，删除原始切片
	for _, fragment := range fragments {
		if err := os.Remove(fragment); err != nil {
			log.Printf("[%s] 合并成功但删除碎片失败: %s, err=%v", cam.ID, fragment, err)
		}
	}

	log.Printf("[%s] 已合并 %s 录像，共 %d 个碎片 -> %s", cam.ID, date, len(fragments), mergedPath)
	return nil
}

func writeConcatList(fragments []string) (string, error) {
	file, err := os.CreateTemp("", "camkeep-merge-*.txt")
	if err != nil {
		return "", err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, fragment := range fragments {
		absFragment, err := filepath.Abs(fragment)
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(writer, "file '%s'\n", escapeConcatPath(absFragment)); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func escapeConcatPath(path string) string {
	return strings.ReplaceAll(path, "'", "'\\''")
}
