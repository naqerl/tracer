package stats

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SystemStats collects system statistics
type SystemStats struct {
	// For CPU calculation between samples
	lastCPUIdle  uint64
	lastCPUTotal uint64
}

// Snapshot gathers current system statistics, skipping any metric that is
// disabled in spec.
func (c *SystemStats) Snapshot(spec Spec) (Stats, error) {
	stats := Stats{
		Timestamp: time.Now().UTC(),
	}

	// Collect CPU usage
	if spec.CPU {
		cpuPercent, err := c.getCPUUsage()
		if err == nil {
			stats.CPUUsagePercent = cpuPercent
		}
	}

	// Collect memory stats
	if spec.Mem {
		memTotal, memUsed, memAvail, err := c.getMemStats()
		if err == nil {
			stats.MemTotalMB = memTotal
			stats.MemUsedMB = memUsed
			stats.MemAvailableMB = memAvail
		}
	}

	// Collect disk stats
	if spec.Disk {
		diskTotal, diskUsed, diskAvail, err := c.getDiskStats()
		if err == nil {
			stats.DiskTotalGB = diskTotal
			stats.DiskUsedGB = diskUsed
			stats.DiskAvailGB = diskAvail
		}
	}

	return stats, nil
}

// getCPUUsage calculates CPU usage percentage since last call
// Reads /proc/stat: cpu user nice system idle iowait irq softirq steal guest guest_nice
func (c *SystemStats) getCPUUsage() (float64, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0, scanner.Err()
	}

	line := scanner.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return 0, nil
	}

	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, nil
	}

	// Parse CPU times
	var total uint64
	var idle uint64

	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseUint(fields[i], 10, 64)
		total += val
		if i == 4 { // idle is the 4th field (index 4, 1-based counting from cpu)
			idle = val
		}
	}

	// Calculate CPU usage since last sample
	var cpuPercent float64
	if c.lastCPUTotal > 0 {
		totalDiff := float64(total - c.lastCPUTotal)
		idleDiff := float64(idle - c.lastCPUIdle)
		if totalDiff > 0 {
			cpuPercent = 100 * (totalDiff - idleDiff) / totalDiff
		}
	}

	c.lastCPUTotal = total
	c.lastCPUIdle = idle

	return cpuPercent, nil
}

// getMemStats reads memory information from /proc/meminfo
// Returns total, used, available in MB
func (c *SystemStats) getMemStats() (total, used, available int64, err error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	defer file.Close()

	var memTotal, memFree, memAvailable, buffers, cached uint64

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)

		switch key {
		case "MemTotal":
			memTotal = val
		case "MemFree":
			memFree = val
		case "MemAvailable":
			memAvailable = val
		case "Buffers":
			buffers = val
		case "Cached":
			cached = val
		}
	}

	// Convert from KB to MB
	total = int64(memTotal / 1024)
	available = int64(memAvailable / 1024)
	if available == 0 {
		// Fallback if MemAvailable not present (older kernels)
		available = int64((memFree + buffers + cached) / 1024)
	}
	used = total - available

	return total, used, available, nil
}

// getDiskStats gets disk usage for the data directory
// Returns total, used, available in GB
func (c *SystemStats) getDiskStats() (total, used, available int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, 0, err
	}

	// Calculate in bytes first, then convert to GB
	blockSize := uint64(stat.Bsize)
	totalBytes := stat.Blocks * blockSize
	freeBytes := stat.Bfree * blockSize
	availBytes := stat.Bavail * blockSize // Available to non-root users

	// Convert to GB (divide by 1024^3)
	const gb = 1024 * 1024 * 1024
	total = int64(totalBytes / gb)
	available = int64(availBytes / gb)
	used = int64((totalBytes - freeBytes) / gb)

	return total, used, available, nil
}
