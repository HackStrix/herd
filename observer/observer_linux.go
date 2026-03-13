//go:build linux

package observer

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// pollNodeStats is the Linux implementation of PollNodeStats.
// It reads /proc/meminfo for memory and /proc/stat twice (100 ms apart)
// for CPU idle.
func pollNodeStats() (NodeStats, error) {
	mem, err := readMemInfo()
	if err != nil {
		return NodeStats{}, fmt.Errorf("observer: readMemInfo: %w", err)
	}

	cpu, err := measureCPUIdle(100 * time.Millisecond)
	if err != nil {
		return NodeStats{}, fmt.Errorf("observer: measureCPUIdle: %w", err)
	}

	return NodeStats{
		TotalMemoryBytes:     mem.total,
		AvailableMemoryBytes: mem.available,
		CPUIdle:              cpu,
	}, nil
}

// ---------------------------------------------------------------------------
// /proc/meminfo
// ---------------------------------------------------------------------------

type memInfo struct {
	total     int64 // bytes
	available int64 // bytes
}

// readMemInfo parses /proc/meminfo and returns MemTotal and MemAvailable.
// Values in the file are in kibibytes (kB); we convert to bytes.
func readMemInfo() (memInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()

	var info memInfo
	found := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() && found < 2 {
		line := scanner.Text()
		var key string
		var val int64
		// Format: "MemTotal:       16384000 kB"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key = strings.TrimSuffix(parts[0], ":")
		val, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		val *= 1024 // kB → bytes
		switch key {
		case "MemTotal":
			info.total = val
			found++
		case "MemAvailable":
			info.available = val
			found++
		}
	}
	if err := scanner.Err(); err != nil {
		return memInfo{}, err
	}
	return info, nil
}

// ---------------------------------------------------------------------------
// /proc/stat CPU idle
// ---------------------------------------------------------------------------

// cpuStat holds the raw CPU tick counters from /proc/stat's "cpu" line.
type cpuStat struct {
	user      int64
	nice      int64
	system    int64
	idle      int64
	iowait    int64
	irq       int64
	softirq   int64
	steal     int64
}

func (s cpuStat) total() int64 {
	return s.user + s.nice + s.system + s.idle + s.iowait + s.irq + s.softirq + s.steal
}

func (s cpuStat) idleTotal() int64 {
	return s.idle + s.iowait
}

// readCPUStat reads the first "cpu" line from /proc/stat.
func readCPUStat() (cpuStat, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuStat{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// "cpu  264230 882 69154 1490779 3965 0 3080 0 0 0"
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return cpuStat{}, fmt.Errorf("observer: unexpected /proc/stat format: %q", line)
		}
		parse := func(i int) int64 {
			v, _ := strconv.ParseInt(fields[i], 10, 64)
			return v
		}
		return cpuStat{
			user:    parse(1),
			nice:    parse(2),
			system:  parse(3),
			idle:    parse(4),
			iowait:  parse(5),
			irq:     parse(6),
			softirq: parse(7),
			steal:   parse(8),
		}, nil
	}
	return cpuStat{}, fmt.Errorf("observer: 'cpu' line not found in /proc/stat")
}

// measureCPUIdle takes two /proc/stat snapshots separated by window and
// returns the fraction of CPU time spent idle (0.0 = fully busy, 1.0 = idle).
func measureCPUIdle(window time.Duration) (float64, error) {
	before, err := readCPUStat()
	if err != nil {
		return 0, err
	}
	time.Sleep(window)
	after, err := readCPUStat()
	if err != nil {
		return 0, err
	}

	totalDelta := after.total() - before.total()
	idleDelta := after.idleTotal() - before.idleTotal()
	if totalDelta <= 0 {
		return 1.0, nil // no ticks elapsed — treat as fully idle
	}
	return float64(idleDelta) / float64(totalDelta), nil
}
