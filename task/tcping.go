package task

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Lyxot/CloudflareSpeedTestDNS/utils"
)

const (
	tcpConnectTimeout = time.Second * 1
	maxRoutine        = 1000
	defaultRoutines   = 200
	defaultPort       = 443
	defaultPingTimes  = 4
)

var (
	Routines  = defaultRoutines
	TCPPort   = defaultPort
	PingTimes = defaultPingTimes
)

type Ping struct {
	wg              *sync.WaitGroup
	m               *sync.Mutex
	ips             []*net.IPAddr
	csv             utils.PingDelaySet
	control         chan bool
	bar             *utils.Bar
	progressCallback ProgressCallback
}

func checkPingDefault() {
	if Routines <= 0 {
		Routines = defaultRoutines
	}
	if TCPPort <= 0 || TCPPort >= 65535 {
		TCPPort = defaultPort
	}
	if PingTimes <= 0 {
		PingTimes = defaultPingTimes
	}
}

func NewPing(progressCallback ProgressCallback) *Ping {
	checkPingDefault()
	ips := loadIPRanges()
	if progressCallback == nil {
		progressCallback = ProgressCallbackFunc
	}
	return &Ping{
		wg:              &sync.WaitGroup{},
		m:               &sync.Mutex{},
		ips:             ips,
		csv:             make(utils.PingDelaySet, 0),
		control:         make(chan bool, Routines),
		bar:             utils.NewBar(len(ips), "可用:", ""),
		progressCallback: progressCallback,
	}
}

func (p *Ping) Run() utils.PingDelaySet {
	if len(p.ips) == 0 {
		return p.csv
	}
	if Httping {
		utils.LogInfo("开始延迟测速（模式：HTTP, 端口：%d, 范围：%v ~ %v ms, 丢包：%.2f）", TCPPort, utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	} else {
		utils.LogInfo("开始延迟测速（模式：TCP, 端口：%d, 范围：%v ~ %v ms, 丢包：%.2f）", TCPPort, utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	}
	// 启动延迟测速前，发送初始进度（告知 Web UI 总 IP 数）
	if p.progressCallback != nil {
		p.progressCallback(len(p.ips), 0)
	}
	for _, ip := range p.ips {
		p.wg.Add(1)
		p.control <- false
		go p.start(ip)
	}
	p.wg.Wait()
	p.bar.Done()
	// 延迟测速完成后，发送最终进度（告知 Web UI 可用 IP 数）
	if p.progressCallback != nil {
		p.progressCallback(len(p.ips), len(p.csv))
	}
	sort.Sort(p.csv)
	return p.csv
}

func (p *Ping) start(ip *net.IPAddr) {
	defer p.wg.Done()
	p.tcpingHandler(ip)
	<-p.control
}

// bool connectionSucceed float32 time
func (p *Ping) tcping(ip *net.IPAddr) (bool, time.Duration) {
	startTime := time.Now()
	var fullAddress string
	if IsIPv4(ip.String()) {
		fullAddress = fmt.Sprintf("%s:%d", ip.String(), TCPPort)
	} else {
		fullAddress = fmt.Sprintf("[%s]:%d", ip.String(), TCPPort)
	}
	conn, err := net.DialTimeout("tcp", fullAddress, tcpConnectTimeout)
	if err != nil {
		return false, 0
	}
	defer func(conn net.Conn) {
		err := conn.Close()
		if err != nil {
			if utils.Debug { // 调试模式下，输出更多信息
				utils.LogError("IP: %s, 关闭 TCP 连接失败，错误信息: %v", ip.String(), err)
			}
		}
	}(conn)
	duration := time.Since(startTime)
	return true, duration
}

// pingReceived pingTotalTime
func (p *Ping) checkConnection(ip *net.IPAddr) (received int, totalDelay time.Duration, colo string) {
	if Httping {
		received, totalDelay, colo = p.httping(ip)
		return
	}
	colo = "" // TCPing 不获取 colo
	for i := 0; i < PingTimes; i++ {
		if ok, delay := p.tcping(ip); ok {
			received++
			totalDelay += delay
		}
	}
	return
}

func (p *Ping) appendIPData(data *utils.PingData) {
	p.m.Lock()
	defer p.m.Unlock()
	p.csv = append(p.csv, utils.CloudflareIPData{
		PingData: data,
	})
}

// handle tcping
func (p *Ping) tcpingHandler(ip *net.IPAddr) {
	// 检查停止请求
	if StopChecker != nil && StopChecker() {
		return
	}
	received, totalDelay, colo := p.checkConnection(ip)
	nowAble := len(p.csv)
	if received != 0 {
		nowAble++
	}
	p.bar.Grow(1, strconv.Itoa(nowAble))
	// 每测试一个 IP 就通知一次进度，用于 Web UI 实时更新可用 IP 数
	if p.progressCallback != nil {
		p.progressCallback(len(p.ips), nowAble)
	}
	if received == 0 {
		return
	}
	data := &utils.PingData{
		IP:          ip,
		Transmitted: PingTimes,
		Received:    received,
		Delay:       totalDelay / time.Duration(received),
		Colo:        colo,
	}
	p.appendIPData(data)
}
