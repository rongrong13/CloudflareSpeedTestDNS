package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lyxot/CloudflareSpeedTestDNS/conf"
	"github.com/Lyxot/CloudflareSpeedTestDNS/ddns"
	"github.com/Lyxot/CloudflareSpeedTestDNS/task"
	"github.com/Lyxot/CloudflareSpeedTestDNS/utils"
	"github.com/gorilla/websocket"
)

//go:embed index.html
var staticFiles embed.FS

var (
	version    string
	gitCommit  string
	configFile string
	webFlag    bool
)

// ==================== Web Server ====================

type SpeedTestProgress struct {
	Stage       string   `json:"stage"`
	Total       int      `json:"total"`
	Current     int      `json:"current"`
	AvailableIP int      `json:"available_ip"`
	Queue       int      `json:"queue"`
	Running     bool     `json:"running"`
	Results     []Result `json:"results"`
}

type Result struct {
	IP            string  `json:"ip"`
	Sent          int     `json:"sent"`
	Received      int     `json:"received"`
	LossRate      float64 `json:"loss_rate"`
	AvgLatency    float64 `json:"avg_latency"`
	DownloadSpeed float64 `json:"download_speed"`
	Region        string  `json:"region"`
}

// WSMessage WebSocket 消息
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// WSConfigRequest 客户端发来的配置
type WSConfigRequest struct {
	MaxDelay   int     `json:"max_delay"`
	MinSpeed   float64 `json:"min_speed"`
	TestCount  int     `json:"test_count"`
	PrintNum   int     `json:"print_num"`
	DisableDL  bool    `json:"disable_download"`
}

// WSScheduleRequest 客户端发来的定时任务配置
type WSScheduleRequest struct {
	Enabled  bool `json:"enabled"`
	Interval int  `json:"interval"` // 小时
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	progress        SpeedTestProgress
	mu              sync.RWMutex
	clients         []*websocket.Conn
	webTestLock     sync.Mutex
	scheduleEnabled bool
	scheduleHours   = 6 // 默认6小时
	nextTestTime    time.Time
	scheduleTimer   *time.Timer

	// 日志环形缓冲区（保留最近1天的日志）
	logBuffer     []string
	logBufferMu   sync.Mutex
	logBufferMax  = 5000 // 最多保留5000条
)

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}
	mu.Lock()
	clients = append(clients, conn)
	mu.Unlock()

	// 发送当前状态
	sendProgress(conn)

	// 发送日志历史
	for _, line := range getLogHistory() {
		m := map[string]string{"type": "log", "msg": line}
		data, _ := json.Marshal(m)
		conn.WriteMessage(websocket.TextMessage, data)
	}

	// 心跳：服务端每 30s ping（用 WriteControl 避免与 broadcast 并发写冲突）
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}()

	defer func() {
		mu.Lock()
		for i, c := range clients {
			if c == conn {
				clients = append(clients[:i], clients[i+1:]...)
				break
			}
		}
		mu.Unlock()
		conn.Close()
	}()

	// 读取客户端消息（配置更新 / 启动测速）
	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg WSMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "config":
			var cfg WSConfigRequest
			if json.Unmarshal(msg.Data, &cfg) == nil {
				applyWebConfig(&cfg)
			}
		case "start":
			if !progress.Running {
				go runWebSpeedTest()
			}
		case "stop":
			if progress.Running {
				atomic.StoreInt32(&stopTestReq, 1)
				utils.LogWarn("收到用户停止请求，正在停止测速...")
			}
		case "schedule":
			var req WSScheduleRequest
			if json.Unmarshal(msg.Data, &req) == nil {
				applySchedule(&req)
			}
		}
	}
}

func applyWebConfig(cfg *WSConfigRequest) {
	if cfg.MaxDelay > 0 {
		conf.CurrentConfig.MaxDelay = cfg.MaxDelay
		utils.InputMaxDelay = time.Duration(cfg.MaxDelay) * time.Millisecond
	}
	if cfg.MinSpeed >= 0 {
		conf.CurrentConfig.MinSpeed = cfg.MinSpeed
		task.MinSpeed = cfg.MinSpeed
	}
	if cfg.TestCount > 0 {
		conf.CurrentConfig.TestCount = cfg.TestCount
		task.TestCount = cfg.TestCount
	}
	if cfg.PrintNum >= 0 {
		conf.CurrentConfig.PrintNum = cfg.PrintNum
		utils.PrintNum = cfg.PrintNum
	}
	conf.CurrentConfig.DisableDownload = cfg.DisableDL
	task.Disable = cfg.DisableDL
}

func applySchedule(req *WSScheduleRequest) {
	if req.Interval > 0 {
		scheduleHours = req.Interval
	}
	scheduleEnabled = req.Enabled
	if scheduleEnabled {
		nextTestTime = time.Now().Add(time.Duration(scheduleHours) * time.Hour)
		startScheduleTimer()
		utils.LogInfo("定时测速已开启，每 %d 小时执行一次，下次: %s", scheduleHours, nextTestTime.Format("15:04:05"))
	} else {
		stopScheduleTimer()
		utils.LogInfo("定时测速已关闭")
	}
	broadcastScheduleState()
}

func startScheduleTimer() {
	stopScheduleTimer()
	remaining := time.Until(nextTestTime)
	if remaining <= 0 {
		remaining = time.Second
	}
	scheduleTimer = time.AfterFunc(remaining, func() {
		if scheduleEnabled && !progress.Running {
			utils.LogInfo("定时任务触发，开始测速...")
			runWebSpeedTest()
			// 测速完成后安排下一次
			nextTestTime = time.Now().Add(time.Duration(scheduleHours) * time.Hour)
			startScheduleTimer()
			broadcastScheduleState()
		}
	})
}

func stopScheduleTimer() {
	if scheduleTimer != nil {
		scheduleTimer.Stop()
		scheduleTimer = nil
	}
}

func broadcastScheduleState() {
	msg := map[string]interface{}{
		"type":            "schedule",
		"enabled":         scheduleEnabled,
		"interval":        scheduleHours,
		"next_test":       nextTestTime.Format("2006-01-02 15:04:05"),
		"next_test_unix":  nextTestTime.Unix(),
		"now_unix":        time.Now().Unix(),
	}
	data, _ := json.Marshal(msg)
	mu.RLock()
	defer mu.RUnlock()
	for _, client := range clients {
		client.WriteMessage(websocket.TextMessage, data)
	}
}

// stopTestReq 用于外部请求停止测速
var stopTestReq int32 // 0=running, 1=stop requested

func runWebSpeedTest() {
	webTestLock.Lock()
	defer webTestLock.Unlock()

	// 原子重置停止标志
	atomic.StoreInt32(&stopTestReq, 0)

	progress.Running = true
	progress.Results = nil
	updateProgressFull("准备中...", 0, 0, 0, 0, nil)

	// panic 恢复：确保 UI 不会永久卡住
	defer func() {
		if r := recover(); r != nil {
			utils.LogError("测速过程发生 panic: %v", r)
			progress.Running = false
			updateProgressFull("测速异常: "+fmt.Sprint(r), 0, 0, 0, 0, nil)
		}
	}()

	log.Println("▶ 开始 Web 测速任务")

	var speedData utils.DownloadSpeedSet
	if task.IsBothMode() {
		origIPv4File := task.IPv4File
		origIPv6File := task.IPv6File
		originOutput := utils.Output

		task.IPv6File = ""
		utils.Output = utils.GetFilenameWithSuffix(originOutput, "ipv4")
		ipv4Data, _ := singleSpeedTest("IPv4", task.ProgressCallbackFunc)
		speedData = append(speedData, ipv4Data...)
		ddnsSync(ipv4Data)

		task.IPv4File = ""
		task.IPv6File = origIPv6File
		utils.Output = utils.GetFilenameWithSuffix(originOutput, "ipv6")
		ipv6Data, _ := singleSpeedTest("IPv6", task.ProgressCallbackFunc)
		speedData = append(speedData, ipv6Data...)
		ddnsSync(ipv6Data)

		task.IPv4File = origIPv4File
		task.IPv6File = origIPv6File
		utils.Output = originOutput
	} else {
		speedData, _ = singleSpeedTest("IP", task.ProgressCallbackFunc)
		ddnsSync(speedData)
	}

	progress.Running = false
	if len(speedData) == 0 {
		log.Println("✅ 测速完成（无结果）")
		updateProgressFull("测速完成（无结果）", 0, 0, 0, 0, nil)
		return
	}

	var results []Result
	for i := 0; i < utils.PrintNum && i < len(speedData); i++ {
		d := speedData[i]
		var lr float64
		if d.Transmitted > 0 {
			lr = float64(d.Transmitted-d.Received) / float64(d.Transmitted)
		}
		results = append(results, Result{
			IP: d.IP.String(), Sent: d.Transmitted, Received: d.Received,
			LossRate: lr, AvgLatency: float64(d.Delay / time.Millisecond),
			DownloadSpeed: d.DownloadSpeed / 1024 / 1024, Region: d.Colo,
		})
	}
	log.Printf("✅ 测速完成，共 %d 个结果\n", len(results))
	updateProgressFull("完成", len(results), len(results), len(results), 0, results)
}

func sendProgress(conn *websocket.Conn) {
	mu.RLock()
	defer mu.RUnlock()
	conn.WriteJSON(progress)
	// 同时发送定时任务状态
	msg := map[string]interface{}{
		"type":           "schedule",
		"enabled":        scheduleEnabled,
		"interval":       scheduleHours,
		"next_test":      nextTestTime.Format("2006-01-02 15:04:05"),
		"next_test_unix": nextTestTime.Unix(),
		"now_unix":       time.Now().Unix(),
	}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// safeWrite 安全地写入 WebSocket，失败时静默忽略
func safeWrite(conn *websocket.Conn, data []byte) bool {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := conn.WriteMessage(websocket.TextMessage, data)
	conn.SetWriteDeadline(time.Time{}) // 清除 deadline
	return err == nil
}

func broadcastProgress() {
	mu.RLock()
	data, _ := json.Marshal(progress)
	conns := make([]*websocket.Conn, len(clients))
	copy(conns, clients)
	mu.RUnlock()
	for _, client := range conns {
		safeWrite(client, data)
	}
}

func appendLog(msg string) {
	logBufferMu.Lock()
	logBuffer = append(logBuffer, msg)
	if len(logBuffer) > logBufferMax {
		logBuffer = logBuffer[len(logBuffer)-logBufferMax:]
	}
	logBufferMu.Unlock()
}

func getLogHistory() []string {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	result := make([]string, len(logBuffer))
	copy(result, logBuffer)
	return result
}

func broadcastLog(msg string) {
	appendLog(msg)
	// 注意：logMessage 本身已写 stdout/stderr（Docker logs 可见），这里只推 WebSocket
	m := map[string]string{"type": "log", "msg": msg}
	data, _ := json.Marshal(m)
	mu.RLock()
	conns := make([]*websocket.Conn, len(clients))
	copy(conns, clients)
	mu.RUnlock()
	for _, c := range conns {
		safeWrite(c, data)
	}
}

func updateProgressFull(stage string, total, current, availableIP, queue int, results []Result) {
	mu.Lock()
	progress.Stage = stage
	progress.Total = total
	progress.Current = current
	progress.AvailableIP = availableIP
	progress.Queue = queue
	if results != nil {
		progress.Results = results
	}
	mu.Unlock()
	broadcastProgress()
}

func setWebCallbacks() {
	// 注册日志钩子，将所有日志转发到 WebSocket
	utils.LogHook = func(level, msg string) {
		broadcastLog(msg)
	}

	// 注册停止检查函数
	task.StopChecker = func() bool {
		return atomic.LoadInt32(&stopTestReq) != 0
	}

	// 延迟测速进度回调 —— 限流：最多每 200ms 广播一次，避免洪水
	var pingLastBroadcast time.Time
	task.ProgressCallbackFunc = func(totalIPs, availableIPs int) {
		now := time.Now()
		// 首次/最后一次/每100个 必发，其余限流 200ms
		if pingLastBroadcast.IsZero() || availableIPs == totalIPs || availableIPs%100 == 0 || now.Sub(pingLastBroadcast) > 200*time.Millisecond {
			pingLastBroadcast = now
			updateProgressFull("延迟测速", totalIPs, totalIPs, availableIPs, 0, nil)
		}
	}
	task.DownloadProgressCallbackFunc = func(queueTotal, current int, speedData utils.DownloadSpeedSet) {
		var results []Result
		for _, d := range speedData {
			var lr float64
			if d.Transmitted > 0 {
				lr = float64(d.Transmitted-d.Received) / float64(d.Transmitted)
			}
			results = append(results, Result{
				IP: d.IP.String(), Sent: d.Transmitted, Received: d.Received,
				LossRate: lr, AvgLatency: float64(d.Delay / time.Millisecond),
				DownloadSpeed: d.DownloadSpeed / 1024 / 1024, Region: d.Colo,
			})
		}
		updateProgressFull("下载测速", queueTotal, current, len(results), queueTotal, results)
	}
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conf.CurrentConfig)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("index.html")
	if err != nil {
		http.Error(w, "无法加载页面", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// gistAPIHandler 处理前端 Gist 上传请求
func gistAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "仅支持 POST"})
		return
	}

	// 获取 token
	gistToken := conf.GistToken
	if gistToken == "" {
		gistToken = os.Getenv("GITHUB_TOKEN")
	}
	if gistToken == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "未配置 GITHUB_TOKEN 或 [gist] token"})
		return
	}

	var req struct {
		Content string `json:"content"`
		Count   int    `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "请求解析失败"})
		return
	}

	if req.Content == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "内容为空"})
		return
	}

	files := map[string]string{"ips.txt": req.Content}
	url, err := uploadToGist(gistToken, files)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	utils.LogInfo("Gist 上传成功: %s (%d 个 IP)", url, req.Count)
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

func startWebServer() {
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/api/config", configHandler)
	http.HandleFunc("/api/gist", gistAPIHandler)
	log.Println("🌐 Web 界面已启动：http://0.0.0.0:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Printf("Web server error: %v", err)
	}
}

// ==================== End Web Server ====================

func init() {
	var printVersion, checkUpdateFlag, debugFlag, pgoFlag bool
	var help = `CloudflareSpeedTestDNS ` + version + `-` + gitCommit + `
测试各个 CDN 或网站所有 IP 的延迟和速度，获取最快 IP (IPv4+IPv6)！
https://github.com/Lyxot/CloudflareSpeedTestDNS

参数：
    -c config.toml
        指定TOML配置文件；默认为config.toml，不存在时使用默认参数
    -web
        启动 Web 界面，监听 http://0.0.0.0:8080 (默认 关闭)
    -debug
        调试输出模式；会在一些非预期情况下输出更多日志以便判断原因；(默认 关闭)
    -pgo
        开启 CPU 性能分析
    -v
        打印程序版本
    -u
        检查版本更新
    -h
        打印帮助说明
`
	flag.BoolVar(&debugFlag, "debug", false, "调试输出模式")
	flag.BoolVar(&pgoFlag, "pgo", false, "开启 CPU 性能分析")
	flag.BoolVar(&webFlag, "web", false, "启动 Web 界面")
	flag.StringVar(&configFile, "c", "", "指定TOML配置文件")
	flag.BoolVar(&printVersion, "v", false, "打印程序版本")
	flag.BoolVar(&checkUpdateFlag, "u", false, "检查版本更新")
	flag.Usage = func() { fmt.Print(help) }
	flag.Parse()

	if pgoFlag {
		pgo()
	}

	if printVersion {
		fmt.Printf("CloudflareSpeedTestDNS version %s, build %s, %s\n", version, gitCommit, runtime.Version())
		endPrint()
		os.Exit(0)
	}

	if checkUpdateFlag {
		fmt.Println("检查版本更新中...")
		versionNew, err := checkUpdate()
		if err != nil {
			_, _ = utils.Red.Printf("检查版本更新失败: %v", err)
		} else if versionNew != "" && versionNew != version {
			_, _ = utils.Yellow.Printf("*** 发现新版本 [%s]！请前往 [https://github.com/Lyxot/CloudflareSpeedTestDNS/releases/latest] 更新！ ***", versionNew)
		} else {
			_, _ = utils.Green.Println("当前为最新版本 [" + version + "]！")
		}
		fmt.Printf("\n")
		endPrint()
		os.Exit(0)
	}

	var config *conf.Config
	var err error

	if configFile != "" {
		config, err = conf.LoadConfig(configFile)
		if err != nil {
			utils.LogFatal("加载配置文件失败: %v", err)
		}
	} else {
		config, err = conf.LoadConfig("config.toml")
		if err != nil {
			utils.LogWarn("加载配置文件 [config.toml] 失败: %v，改用默认配置", err)
			config = conf.CreateDefaultConfig()
		}
	}

	conf.LoadEnvConfig(config)
	conf.ApplyConfig(config)

	// 设置 Web UI 进度回调（无论是否启动 Web 界面，都设置回调，避免 nil 引用）
	setWebCallbacks()

	flag.Visit(func(f *flag.Flag) {
		if f.Name == "debug" {
			utils.Debug = debugFlag
		}
	})

	if err := utils.InitLogFile(); err != nil {
		utils.LogFatal("初始化日志文件失败: %v", err)
	}

	if task.MinSpeed > 0 && config.MaxDelay == 9999 {
		utils.LogWarn("配置了 min_speed 参数时，建议搭配 max_delay 参数，以避免因凑不够 test_count 数量而一直测速...")
	}
}

func main() {
	utils.LogInfo("# Lyxot/CloudflareSpeedTestDNS %s-%s", version, gitCommit)

	if webFlag {
		// Web 模式：启动 Web 服务器（阻塞主 goroutine）
		// 定时任务通过 Web UI 控制，不需要 cron() 死循环
		startWebServer()
	} else {
		// 命令行模式：执行一次测速
		_, _ = speedTest()

		// 上传结果到 Gist
		gistToken := conf.GistToken
		if gistToken == "" {
			gistToken = os.Getenv("GITHUB_TOKEN")
		}
		if conf.EnableGist || gistToken != "" {
			uploadResultsIfNeeded(gistToken)
		}

		endPrint()
	}
}


func speedTest() ([]string, error) {
	var ipData []string
	var err error
	if task.IsBothMode() {
		origIPv4File := task.IPv4File
		origIPv6File := task.IPv6File
		originOutput := utils.Output

		utils.LogInfo("[IPv4] 开始测试IPv4...")
		task.IPv6File = ""
		utils.Output = utils.GetFilenameWithSuffix(originOutput, "ipv4")
		ipv4SpeedData, testErr := singleSpeedTest("IPv4", task.ProgressCallbackFunc)
		if testErr == nil {
			ipData = append(ipData, ddnsSync(ipv4SpeedData)...)
		} else {
			err = testErr
		}

		utils.LogInfo("[IPv6] 开始测试IPv6...")
		task.IPv4File = ""
		task.IPv6File = origIPv6File
		utils.Output = utils.GetFilenameWithSuffix(originOutput, "ipv6")
		ipv6SpeedData, testErr := singleSpeedTest("IPv6", task.ProgressCallbackFunc)
		if testErr == nil {
			ipData = append(ipData, ddnsSync(ipv6SpeedData)...)
		} else {
			err = errors.Join(err, testErr)
		}

		task.IPv4File = origIPv4File
		task.IPv6File = origIPv6File
		utils.Output = originOutput
	} else {
		speedData, testErr := singleSpeedTest("IP", task.ProgressCallbackFunc)
		if testErr == nil {
			ipData = ddnsSync(speedData)
		} else {
			err = testErr
		}
	}
	return ipData, err
}

func singleSpeedTest(ipVersion string, progressCallback task.ProgressCallback) (utils.DownloadSpeedSet, error) {
	var speedData utils.DownloadSpeedSet
	for i := 0; i < conf.MaxAttempts; i++ {
		pingData := task.NewPing(progressCallback).Run().FilterDelay().FilterLossRate()
		speedData = task.TestDownloadSpeed(pingData)
		if len(speedData) >= conf.MinNum {
			break
		}
		if i < conf.MaxAttempts-1 {
			utils.LogWarn("符合条件的%s数量[%d]少于设定的最小数量[%d]，将在15秒后开始新一轮测试...", ipVersion, len(speedData), conf.MinNum)
			time.Sleep(15 * time.Second)
		} else {
			utils.LogWarn("符合条件的%s数量[%d]少于设定的最小数量[%d]，已达到最大重试次数[%d]，测试结束。", ipVersion, len(speedData), conf.MinNum, conf.MaxAttempts)
			return speedData, fmt.Errorf("符合条件的%s数量少于设定的最小数量", ipVersion)
		}
	}
	utils.ExportCsv(speedData)
	speedData.Print()

	// 测速完成，通知 Web UI 显示最终结果
	if progressCallback != nil {
		var results []Result
		printNum := utils.PrintNum
		if printNum > len(speedData) {
			printNum = len(speedData)
		}
		for i := 0; i < printNum; i++ {
			data := speedData[i]
			// 计算丢包率
			var lossRate float64
			if data.Transmitted > 0 {
				lossRate = float64(data.Transmitted-data.Received) / float64(data.Transmitted)
			}
			results = append(results, Result{
				IP:            data.IP.String(),
				Sent:          data.Transmitted,
				Received:      data.Received,
				LossRate:      lossRate,
				AvgLatency:    float64(data.Delay / time.Millisecond),
				DownloadSpeed: data.DownloadSpeed / 1024 / 1024,
				Region:        data.Colo,
			})
		}
		updateProgressFull("完成", len(speedData), len(speedData), len(speedData), 0, results)
	}

	return speedData, nil
}

func ddnsSync(speedData utils.DownloadSpeedSet) []string {
	if len(speedData) == 0 {
		return []string{}
	}

	var ipv4Results []string
	var ipv6Results []string
	for i := 0; i < utils.PrintNum && i < len(speedData); i++ {
		ip := speedData[i].IP.String()
		if task.IsIPv4(ip) {
			ipv4Results = append(ipv4Results, ip)
		} else {
			ipv6Results = append(ipv6Results, ip)
		}
	}

	if conf.EnableAliDNS {
		utils.LogInfo("开始同步结果到阿里云DNS...")
		if err := ddns.SyncDNSRecords(ipv4Results, ipv6Results); err != nil {
			utils.LogError("同步到阿里云DNS失败: %v", err)
		} else {
			utils.LogInfo("同步到阿里云DNS成功!")
		}
	}

	if conf.EnableDNSPod {
		utils.LogInfo("开始同步结果到DNSPod DNS...")
		if err := ddns.SyncDNSPodRecords(ipv4Results, ipv6Results); err != nil {
			utils.LogError("同步到DNSPod DNS失败: %v", err)
		} else {
			utils.LogInfo("同步到DNSPod DNS成功!")
		}
	}

	if conf.EnableCloudflare {
		utils.LogInfo("开始同步结果到Cloudflare DNS...")
		if err := ddns.SyncCloudflareRecords(ipv4Results, ipv6Results); err != nil {
			utils.LogError("同步到Cloudflare DNS失败: %v", err)
		} else {
			utils.LogInfo("同步到Cloudflare DNS成功!")
		}
	}

	if conf.EnableCFKV {
		utils.LogInfo("开始同步结果到Cloudflare KV...")
		if err := ddns.SyncCloudflareKV(speedData.FilterIPv4(), speedData.FilterIPv6()); err != nil {
			utils.LogError("同步到Cloudflare KV失败: %v", err)
		} else {
			utils.LogInfo("同步到Cloudflare KV成功!")
		}
	}

	return append(ipv4Results, ipv6Results...)
}

func endPrint() {
	if utils.NoPrintResult() {
		return
	}
	if runtime.GOOS == "windows" {
		fmt.Println("按下 回车键 或 Ctrl+C 退出。")
		_, _ = fmt.Scanln()
	}
}

func checkUpdate() (string, error) {
	timeout := 10 * time.Second
	client := http.Client{Timeout: timeout}
	res, err := client.Get("https://api.github.com/repos/Lyxot/CloudflareSpeedTestDNS/releases/latest")
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			utils.LogError("关闭版本检查响应流失败，错误信息: %v", err)
		}
	}(res.Body)

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	if tagName, ok := result["tag_name"].(string); ok {
		return tagName, nil
	}
	return "", fmt.Errorf("can't get tag_name from github api")
}

func pgo() {
	f, err := os.Create("cpu.pprof")
	if err != nil {
		utils.LogFatal("could not create CPU profile: %v", err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			utils.LogFatal("could not close CPU profile: %v", err)
		}
	}(f)
	if err := pprof.StartCPUProfile(f); err != nil {
		utils.LogFatal("could not start CPU profile: %v", err)
	}
	defer pprof.StopCPUProfile()
}
