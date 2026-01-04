// Package collector provides system metrics collection for Linux nodes.
// It abstracts hardware-specific details while optimizing for edge/resource-constrained environments.
package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeMetrics represents a snapshot of node health metrics.
type NodeMetrics struct {
	Timestamp time.Time `json:"timestamp"`
	NodeName  string    `json:"node_name"`

	// CPU metrics
	CPUTemperature   float64 `json:"cpu_temperature_celsius"`
	CPUUsagePercent  float64 `json:"cpu_usage_percent"`
	CPUThrottled     bool    `json:"cpu_throttled"`
	CPUFrequencyMHz  float64 `json:"cpu_frequency_mhz"`
	LoadAverage1Min  float64 `json:"load_average_1min"`
	LoadAverage5Min  float64 `json:"load_average_5min"`
	LoadAverage15Min float64 `json:"load_average_15min"`

	// Memory metrics
	MemoryTotalBytes     uint64  `json:"memory_total_bytes"`
	MemoryAvailableBytes uint64  `json:"memory_available_bytes"`
	MemoryUsagePercent   float64 `json:"memory_usage_percent"`
	SwapTotalBytes       uint64  `json:"swap_total_bytes"`
	SwapUsedBytes        uint64  `json:"swap_used_bytes"`
	OOMKillCount         uint64  `json:"oom_kill_count"`

	// Disk metrics
	DiskTotalBytes   uint64  `json:"disk_total_bytes"`
	DiskUsedBytes    uint64  `json:"disk_used_bytes"`
	DiskUsagePercent float64 `json:"disk_usage_percent"`
	DiskIOReadBytes  uint64  `json:"disk_io_read_bytes"`
	DiskIOWriteBytes uint64  `json:"disk_io_write_bytes"`
	DiskIOLatencyMs  float64 `json:"disk_io_latency_ms"`

	// Network metrics
	NetworkRxBytes   uint64  `json:"network_rx_bytes"`
	NetworkTxBytes   uint64  `json:"network_tx_bytes"`
	NetworkRxErrors  uint64  `json:"network_rx_errors"`
	NetworkTxErrors  uint64  `json:"network_tx_errors"`
	NetworkLatencyMs float64 `json:"network_latency_ms"`

	// Collection metadata
	CollectionDurationMs float64  `json:"collection_duration_ms"`
	Errors               []string `json:"errors,omitempty"`
}

// Collector gathers system metrics from a Linux node.
type Collector struct {
	nodeName     string
	procPath     string
	sysPath      string
	thermalZones []string
	primaryDisk  string
	networkIface string

	// Previous values for delta calculations
	mu            sync.Mutex
	prevCPUStats  cpuStats
	prevDiskStats diskStats
	prevNetStats  netStats
	lastCollect   time.Time
}

type cpuStats struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
}

type diskStats struct {
	readBytes  uint64
	writeBytes uint64
	readTime   uint64
	writeTime  uint64
	ioTime     uint64
}

type netStats struct {
	rxBytes  uint64
	txBytes  uint64
	rxErrors uint64
	txErrors uint64
}

// Option configures the Collector.
type Option func(*Collector)

// WithProcPath sets a custom /proc path (useful for testing).
func WithProcPath(path string) Option {
	return func(c *Collector) {
		c.procPath = path
	}
}

// WithSysPath sets a custom /sys path (useful for testing).
func WithSysPath(path string) Option {
	return func(c *Collector) {
		c.sysPath = path
	}
}

// WithDisk sets the primary disk device to monitor.
func WithDisk(device string) Option {
	return func(c *Collector) {
		c.primaryDisk = device
	}
}

// WithNetworkInterface sets the primary network interface to monitor.
func WithNetworkInterface(iface string) Option {
	return func(c *Collector) {
		c.networkIface = iface
	}
}

// New creates a new Collector for the given node.
func New(nodeName string, opts ...Option) (*Collector, error) {
	c := &Collector{
		nodeName: nodeName,
		procPath: "/proc",
		sysPath:  "/sys",
	}

	for _, opt := range opts {
		opt(c)
	}

	// Discover thermal zones
	c.thermalZones = c.discoverThermalZones()

	// Auto-detect primary disk if not specified
	if c.primaryDisk == "" {
		c.primaryDisk = c.detectPrimaryDisk()
	}

	// Auto-detect primary network interface if not specified
	if c.networkIface == "" {
		c.networkIface = c.detectPrimaryInterface()
	}

	return c, nil
}

// Collect gathers all metrics from the node.
func (c *Collector) Collect(ctx context.Context) (*NodeMetrics, error) {
	start := time.Now()

	m := &NodeMetrics{
		Timestamp: start,
		NodeName:  c.nodeName,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	addError := func(err error) {
		mu.Lock()
		m.Errors = append(m.Errors, err.Error())
		mu.Unlock()
	}

	// Collect metrics concurrently
	wg.Add(5)

	go func() {
		defer wg.Done()
		if err := c.collectCPUMetrics(m); err != nil {
			addError(fmt.Errorf("cpu: %w", err))
		}
	}()

	go func() {
		defer wg.Done()
		if err := c.collectMemoryMetrics(m); err != nil {
			addError(fmt.Errorf("memory: %w", err))
		}
	}()

	go func() {
		defer wg.Done()
		if err := c.collectDiskMetrics(m); err != nil {
			addError(fmt.Errorf("disk: %w", err))
		}
	}()

	go func() {
		defer wg.Done()
		if err := c.collectNetworkMetrics(m); err != nil {
			addError(fmt.Errorf("network: %w", err))
		}
	}()

	go func() {
		defer wg.Done()
		if err := c.collectThermalMetrics(m); err != nil {
			addError(fmt.Errorf("thermal: %w", err))
		}
	}()

	wg.Wait()

	m.CollectionDurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	return m, nil
}

func (c *Collector) collectCPUMetrics(m *NodeMetrics) error {
	// Read /proc/stat for CPU usage
	f, err := os.Open(filepath.Join(c.procPath, "stat"))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) >= 8 {
				stats := cpuStats{}
				stats.user, _ = strconv.ParseUint(fields[1], 10, 64)
				stats.nice, _ = strconv.ParseUint(fields[2], 10, 64)
				stats.system, _ = strconv.ParseUint(fields[3], 10, 64)
				stats.idle, _ = strconv.ParseUint(fields[4], 10, 64)
				stats.iowait, _ = strconv.ParseUint(fields[5], 10, 64)
				stats.irq, _ = strconv.ParseUint(fields[6], 10, 64)
				stats.softirq, _ = strconv.ParseUint(fields[7], 10, 64)

				c.mu.Lock()
				if c.prevCPUStats.idle > 0 {
					prevTotal := c.prevCPUStats.user + c.prevCPUStats.nice + c.prevCPUStats.system +
						c.prevCPUStats.idle + c.prevCPUStats.iowait + c.prevCPUStats.irq + c.prevCPUStats.softirq
					currTotal := stats.user + stats.nice + stats.system + stats.idle +
						stats.iowait + stats.irq + stats.softirq

					totalDiff := float64(currTotal - prevTotal)
					idleDiff := float64(stats.idle - c.prevCPUStats.idle)

					if totalDiff > 0 {
						m.CPUUsagePercent = 100.0 * (1.0 - idleDiff/totalDiff)
					}
				}
				c.prevCPUStats = stats
				c.mu.Unlock()
			}
			break
		}
	}

	// Read load average from /proc/loadavg
	loadavg, err := os.ReadFile(filepath.Join(c.procPath, "loadavg"))
	if err == nil {
		fields := strings.Fields(string(loadavg))
		if len(fields) >= 3 {
			m.LoadAverage1Min, _ = strconv.ParseFloat(fields[0], 64)
			m.LoadAverage5Min, _ = strconv.ParseFloat(fields[1], 64)
			m.LoadAverage15Min, _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	// Read CPU frequency from /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq
	freqPath := filepath.Join(c.sysPath, "devices/system/cpu/cpu0/cpufreq/scaling_cur_freq")
	if freq, err := os.ReadFile(freqPath); err == nil {
		if khz, err := strconv.ParseUint(strings.TrimSpace(string(freq)), 10, 64); err == nil {
			m.CPUFrequencyMHz = float64(khz) / 1000.0
		}
	}

	// Check for CPU throttling
	throttlePath := filepath.Join(c.sysPath, "devices/system/cpu/cpu0/thermal_throttle/core_throttle_count")
	if throttle, err := os.ReadFile(throttlePath); err == nil {
		if count, err := strconv.ParseUint(strings.TrimSpace(string(throttle)), 10, 64); err == nil {
			m.CPUThrottled = count > 0
		}
	}

	return nil
}

func (c *Collector) collectMemoryMetrics(m *NodeMetrics) error {
	f, err := os.Open(filepath.Join(c.procPath, "meminfo"))
	if err != nil {
		return err
	}
	defer f.Close()

	var memTotal, memAvailable, swapTotal, swapFree uint64

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		value, _ := strconv.ParseUint(fields[1], 10, 64)
		value *= 1024 // Convert from KB to bytes

		switch fields[0] {
		case "MemTotal:":
			memTotal = value
		case "MemAvailable:":
			memAvailable = value
		case "SwapTotal:":
			swapTotal = value
		case "SwapFree:":
			swapFree = value
		}
	}

	m.MemoryTotalBytes = memTotal
	m.MemoryAvailableBytes = memAvailable
	if memTotal > 0 {
		m.MemoryUsagePercent = 100.0 * float64(memTotal-memAvailable) / float64(memTotal)
	}
	m.SwapTotalBytes = swapTotal
	m.SwapUsedBytes = swapTotal - swapFree

	// Read OOM kill count from /proc/vmstat
	vmstat, err := os.Open(filepath.Join(c.procPath, "vmstat"))
	if err == nil {
		defer vmstat.Close()
		scanner := bufio.NewScanner(vmstat)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "oom_kill ") {
				fields := strings.Fields(scanner.Text())
				if len(fields) >= 2 {
					m.OOMKillCount, _ = strconv.ParseUint(fields[1], 10, 64)
				}
				break
			}
		}
	}

	return nil
}

func (c *Collector) collectDiskMetrics(m *NodeMetrics) error {
	if c.primaryDisk == "" {
		return nil
	}

	// Read disk stats from /proc/diskstats
	f, err := os.Open(filepath.Join(c.procPath, "diskstats"))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}

		if fields[2] == c.primaryDisk {
			stats := diskStats{}
			// Field 5: sectors read (multiply by 512 for bytes)
			sectorsRead, _ := strconv.ParseUint(fields[5], 10, 64)
			stats.readBytes = sectorsRead * 512
			// Field 6: time spent reading (ms)
			stats.readTime, _ = strconv.ParseUint(fields[6], 10, 64)
			// Field 9: sectors written
			sectorsWritten, _ := strconv.ParseUint(fields[9], 10, 64)
			stats.writeBytes = sectorsWritten * 512
			// Field 10: time spent writing (ms)
			stats.writeTime, _ = strconv.ParseUint(fields[10], 10, 64)
			// Field 12: time spent doing I/O (ms)
			stats.ioTime, _ = strconv.ParseUint(fields[12], 10, 64)

			c.mu.Lock()
			if c.prevDiskStats.readBytes > 0 || c.prevDiskStats.writeBytes > 0 {
				m.DiskIOReadBytes = stats.readBytes - c.prevDiskStats.readBytes
				m.DiskIOWriteBytes = stats.writeBytes - c.prevDiskStats.writeBytes

				ioTimeDiff := stats.ioTime - c.prevDiskStats.ioTime
				elapsed := time.Since(c.lastCollect).Milliseconds()
				if elapsed > 0 {
					m.DiskIOLatencyMs = float64(ioTimeDiff) / float64(elapsed) * 100
				}
			}
			c.prevDiskStats = stats
			c.lastCollect = time.Now()
			c.mu.Unlock()
			break
		}
	}

	return nil
}

func (c *Collector) collectNetworkMetrics(m *NodeMetrics) error {
	if c.networkIface == "" {
		return nil
	}

	// Read network stats from /proc/net/dev
	f, err := os.Open(filepath.Join(c.procPath, "net/dev"))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, c.networkIface+":") {
			fields := strings.Fields(line)
			if len(fields) < 17 {
				continue
			}

			// Find the interface name and parse stats
			for i, field := range fields {
				if strings.HasSuffix(field, ":") || strings.Contains(field, c.networkIface) {
					// Stats start after the interface name
					startIdx := i + 1
					if strings.HasSuffix(field, ":") {
						startIdx = i + 1
					} else {
						// Interface name might be combined with first stat
						parts := strings.Split(field, ":")
						if len(parts) == 2 {
							fields[i+1] = parts[1]
							startIdx = i + 1
						}
					}

					if startIdx+10 <= len(fields) {
						stats := netStats{}
						stats.rxBytes, _ = strconv.ParseUint(fields[startIdx], 10, 64)
						stats.rxErrors, _ = strconv.ParseUint(fields[startIdx+2], 10, 64)
						stats.txBytes, _ = strconv.ParseUint(fields[startIdx+8], 10, 64)
						stats.txErrors, _ = strconv.ParseUint(fields[startIdx+10], 10, 64)

						c.mu.Lock()
						if c.prevNetStats.rxBytes > 0 || c.prevNetStats.txBytes > 0 {
							m.NetworkRxBytes = stats.rxBytes - c.prevNetStats.rxBytes
							m.NetworkTxBytes = stats.txBytes - c.prevNetStats.txBytes
							m.NetworkRxErrors = stats.rxErrors - c.prevNetStats.rxErrors
							m.NetworkTxErrors = stats.txErrors - c.prevNetStats.txErrors
						}
						c.prevNetStats = stats
						c.mu.Unlock()
					}
					break
				}
			}
		}
	}

	return nil
}

func (c *Collector) collectThermalMetrics(m *NodeMetrics) error {
	if len(c.thermalZones) == 0 {
		return nil
	}

	var maxTemp float64
	for _, zone := range c.thermalZones {
		tempPath := filepath.Join(zone, "temp")
		if temp, err := os.ReadFile(tempPath); err == nil {
			if millidegrees, err := strconv.ParseInt(strings.TrimSpace(string(temp)), 10, 64); err == nil {
				tempC := float64(millidegrees) / 1000.0
				if tempC > maxTemp {
					maxTemp = tempC
				}
			}
		}
	}
	m.CPUTemperature = maxTemp

	return nil
}

func (c *Collector) discoverThermalZones() []string {
	var zones []string
	thermalPath := filepath.Join(c.sysPath, "class/thermal")

	entries, err := os.ReadDir(thermalPath)
	if err != nil {
		return zones
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "thermal_zone") {
			zonePath := filepath.Join(thermalPath, entry.Name())
			// Check if this zone has a temp file
			if _, err := os.Stat(filepath.Join(zonePath, "temp")); err == nil {
				zones = append(zones, zonePath)
			}
		}
	}

	return zones
}

func (c *Collector) detectPrimaryDisk() string {
	// Try to find the root filesystem disk
	mounts, err := os.ReadFile(filepath.Join(c.procPath, "mounts"))
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "/" {
			device := fields[0]
			// Extract device name (e.g., /dev/sda1 -> sda)
			device = strings.TrimPrefix(device, "/dev/")
			// Remove partition number
			for len(device) > 0 && device[len(device)-1] >= '0' && device[len(device)-1] <= '9' {
				device = device[:len(device)-1]
			}
			// Handle nvme devices (e.g., nvme0n1p1 -> nvme0n1)
			if strings.HasPrefix(device, "nvme") && strings.HasSuffix(device, "p") {
				device = device[:len(device)-1]
			}
			return device
		}
	}

	return ""
}

func (c *Collector) detectPrimaryInterface() string {
	// Read the default route to find primary interface
	routePath := filepath.Join(c.procPath, "net/route")
	f, err := os.Open(routePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan() // Skip header

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == "00000000" {
			// This is the default route
			return fields[0]
		}
	}

	return ""
}
