# 电视哨兵 (tv_eye)

[简体中文](./README.md) | [English](./README_en.md)

---

## 为什么做这个项目

给娃写了第一个程序 -- 电视哨兵

每次看电视被儿童锁锁住就找爷爷奶奶解锁，看个不停。

于是 vibe coding，调用家里的监控摄像头实时盯着电视，一旦连续播放超时，电视自动关闭，音箱同步提醒。冷却期间手动打开会被提醒并自动关闭电视。

---

## 这是什么

电视哨兵是一款基于 Go 语言开发的家庭电视监管工具，深度集成 **go2rtc** 和 **FFmpeg**。它利用家中已有的 RTSP 摄像头，通过计算机视觉实时检测电视屏幕状态，结合 Home Assistant 实现自动关机和语音提醒，专为需要管控孩子看电视时间的家长设计。

同时，它也是一款轻量级私有化 NVR（网络视频录像机），适合部署在家庭 NAS（飞牛、群晖、威联通、Unraid 等）和低功耗小主机上。

## 电视哨兵功能详解

电视哨兵的核心能力是**用摄像头"看住"电视**：通过图像分析判断电视是否在播放，结合时间规则自动执行关机和提醒。

### 工作原理

```
摄像头 RTSP 流
    │
    ▼
┌─────────────┐    ROI 透视校正     ┌──────────────┐
│  画面采集    │ ──────────────────▶ │  电视状态检测  │
│  (go2rtc)   │                     │  (HSV+边缘+帧差) │
└─────────────┘                     └──────┬───────┘
                                           │ rawOn/Off
                                           ▼
                                    ┌──────────────┐
                                    │   状态机      │
                                    │ OFF→PENDING   │
                                    │ →TRIGGERED    │
                                    │ →RESTING      │
                                    └──────┬───────┘
                                           │ 超时/违规
                                           ▼
                              ┌──────────────────────────┐
                              │  Home Assistant 联动      │
                              │  1. 音箱语音提醒           │
                              │  2. 红外遥控关机           │
                              │  3. 微信通知家长           │
                              └──────────────────────────┘
```

### 检测机制

- **ROI 透视校正**：在摄像头画面中框选电视区域，支持手动配置四个角点坐标，也支持自动校准（识别画面中最大的矩形轮廓）
- **多维度图像分析**：通过 HSV 亮度（V）、饱和度（S）、拉普拉斯边缘（Laplacian）、帧间差异（Frame Diff）四个维度综合判断电视是否在播放
- **基线自校准**：在电视关闭状态下采集多帧基线数据，自适应调整检测阈值，适应不同环境光线
- **防抖状态机**：连续多帧确认后才切换状态，避免闪烁误判

### 时间管控规则

| 规则 | 默认值 | 说明 |
|------|--------|------|
| 单次观看上限 | 5 分钟 | 连续播放超时后自动关机 |
| 冷却休息时间 | 20 分钟 | 关机后强制休息，期间开机会被再次关闭 |
| 每日总时长上限 | 60 分钟 | 超过后锁定至次日零点 |
| 监控时段 | 08:00-23:00 | 只在指定时间段内生效 |

### Home Assistant 联动

通过 Home Assistant API 实现三重管控：

1. **音箱语音提醒** -- 超时时通过小爱音箱等设备播报提醒文本
2. **红外遥控关机** -- 通过红外发射器（如 Broadlink）模拟遥控器关机
3. **微信通知** -- 每次执行关机操作时推送微信消息给家长

### Web 监控面板

- 实时显示电视状态（ON/OFF）、当前会话时长、今日累计时长
- 倒计时显示距离超时关机的剩余时间
- 冷却期剩余时间实时更新
- 操作日志记录所有事件（开机、关机、超时、违规等）
- 电视画面 ROI 区域实时预览

---

## NVR 录像功能

除了电视哨兵，CamKeep 也是一款完整的 NVR 录像系统：

* **单容器极简部署**：内置 go2rtc 与 FFmpeg，Docker 启动即可使用；Web 控制台支持热更新配置。
* **纯内网私有运行**：不依赖云端、不强制账号。视频流和录像文件都留在你的局域网与 NAS 内。
* **有 RTSP 就能接入**：兼容海康、大华、TP-Link、刷机摄像头等各类 RTSP 视频源。
* **多种录像模式**：支持定时录像、手动启停、动检录像、延时摄影、TS/MP4 切片、按天回放。
* **自动存储管理**：通过 `retention_days` 控制保留天数，后台自动清理过期录像。
* **WebRTC 低延迟直播**：支持 4/6 宫格预览、双击全屏、设备状态和日期回放。
* **适合 NAS 与边缘设备**：原生支持 x86-64 与 ARM64，适配群晖、威联通、Unraid、飞牛、树莓派等。

---

## 极速部署

### 1. 准备配置文件

在你的 NAS 上创建配置目录（例如 `/vol1/CamKeep`），新建 `config/conf.yaml`：

具体配置项说明，请阅览：[配置说明文档 (conf_usage.md)](https://github.com/r0n9/camkeep/blob/main/conf_usage.md)

```yaml
daily_merge:
  enabled: false          # 是否每天合并前一天碎片录像
  time: "03:30"           # 合并时间

cameras:
  - id: "living-room"     # 摄像头唯一ID
    rtsp_url: "rtsp://admin:123456@192.168.1.100:554/stream"
    retention_days: 7
    segment_duration: 300
    format: "ts"
    record_time: "00:00-24:00"
    mode: "normal"
    motion_detect: false

tv_monitors:
  - camera_id: "living-room"          # 对应上面的摄像头 ID
    enabled: true
    monitor_time: "08:00-23:00"       # 监控时段
    target_duration: 300              # 单次观看上限 (秒)
    max_session_minutes: 5            # 单次观看上限 (分钟)
    rest_minutes: 20                  # 冷却休息时间 (分钟)
    max_daily_minutes: 60             # 每日总时长上限 (分钟)
    roi_auto_calibrate: true          # 自动识别电视区域
    ha_url: "http://192.168.1.200:8123"  # Home Assistant 地址
    ha_token: "你的长期访问令牌"
    ha_tts_service: "notify.xiaomi_cn_xxx"  # 小爱音箱
    ha_tts_message: "看电视时间到了，休息一下吧！"
    ha_ir_turn_off_button: "button.tv_remote_power"  # 红外关机按钮
    ha_notify_service: "hassbox_notify.hassbox_notify"  # 微信通知
```

### 2. 启动服务

**Docker Run（推荐）：**

```bash
docker run -d \
  --name camkeep \
  --restart unless-stopped \
  --network host \
  -e TZ=Asia/Shanghai \
  -v ${PWD}/config:/app/config \
  -v ${PWD}/records:/app/records \
  r0n9/camkeep:latest
```

**Docker-Compose：**

```yaml
services:
  camkeep:
    image: r0n9/camkeep:latest
    container_name: camkeep
    restart: unless-stopped
    network_mode: "host"
    environment:
      - TZ=Asia/Shanghai
    volumes:
      - ./config:/app/config
      - ./records:/app/records
```

### 3. 开始使用

启动成功后，浏览器访问 `http://<你的NAS IP>:9110` 即可进入监控中心。

---

## 致谢

感谢 [r0n9](https://github.com/r0n9) 提供的 [CamKeep](https://github.com/r0n9/camkeep) 原始项目，电视哨兵是在其基础上扩展开发的。

## 开源协议

本项目基于 **MIT License** 开源。欢迎大家提交 Issue 和 PR 共同完善。

This project uses:

- go2rtc -- https://github.com/AlexxIT/go2rtc
  Licensed under the MIT License.
