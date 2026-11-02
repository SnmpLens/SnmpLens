package network

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"SnmpLens/pkg/netaddr"

	probing "github.com/prometheus-community/pro-bing"
)

// PingResult holds ping statistics from pro-bing.
type PingResult struct {
	Target      string    `json:"target"`
	Sent        int       `json:"sent"`
	Received    int       `json:"received"`
	LossPercent float64   `json:"lossPercent"`
	MinMs       float64   `json:"minMs"`
	AvgMs       float64   `json:"avgMs"`
	MaxMs       float64   `json:"maxMs"`
	Replies     []float64 `json:"replies"`
}

// TracerouteHop represents a single hop in a traceroute.
type TracerouteHop struct {
	Hop     int    `json:"hop"`
	IP      string `json:"ip"`
	RTT1    string `json:"rtt1"`
	RTT2    string `json:"rtt2"`
	RTT3    string `json:"rtt3"`
	Timeout bool   `json:"timeout"`
}

// Ping uses pro-bing for cross-platform ICMP ping without elevated privileges.
func Ping(target string, count int) (PingResult, error) {
	if count <= 0 {
		count = 4
	}
	// A target that names its SNMP port ("10.0.0.5:1161") is still that host.
	target, _ = netaddr.SplitTarget(target, 0)
	result := PingResult{Target: target}
	if err := netaddr.ValidTarget(target); err != nil {
		return result, err
	}

	// pro-bing resolves the name and picks ICMPv4 or ICMPv6 from the answer,
	// so IPv6 needs nothing here beyond an address it can parse.
	pinger, err := probing.NewPinger(target)
	if err != nil {
		return result, fmt.Errorf("resolve target: %w", err)
	}

	pinger.Count = count
	pinger.Timeout = time.Duration(count)*time.Second + 5*time.Second

	// On Windows, use privileged mode (uses Windows ICMP API internally)
	if runtime.GOOS == "windows" {
		pinger.SetPrivileged(true)
	}

	// Collect individual RTTs
	pinger.OnRecv = func(pkt *probing.Packet) {
		result.Replies = append(result.Replies, float64(pkt.Rtt.Microseconds())/1000.0)
	}

	if err := pinger.Run(); err != nil {
		return result, fmt.Errorf("ping failed: %w", err)
	}

	stats := pinger.Statistics()
	result.Sent = stats.PacketsSent
	result.Received = stats.PacketsRecv
	result.LossPercent = stats.PacketLoss
	result.MinMs = float64(stats.MinRtt.Microseconds()) / 1000.0
	result.AvgMs = float64(stats.AvgRtt.Microseconds()) / 1000.0
	result.MaxMs = float64(stats.MaxRtt.Microseconds()) / 1000.0

	return result, nil
}

// Traceroute uses system traceroute/tracert command with OS-specific arguments.
// emit is called once per hop, as it is parsed.
func Traceroute(ctx context.Context, target string, emit func(TracerouteHop)) ([]TracerouteHop, error) {
	// A target that names its SNMP port ("10.0.0.5:1161") is still that host.
	target, _ = netaddr.SplitTarget(target, 0)
	// Checked before it becomes argv. There is no shell here, so nothing can
	// be injected as a command — but a value starting with a dash IS read as
	// an option by tracert and by traceroute, and nothing checked.
	if err := netaddr.ValidTarget(target); err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		// tracert reads the family from the address itself.
		cmd = exec.CommandContext(ctx, "tracert", "-d", "-w", "2000", target)
	case runtime.GOOS == "darwin" && isIPv6Target(target):
		// macOS ships traceroute as IPv4-only and puts v6 in a SEPARATE
		// binary; asking traceroute for an IPv6 address there just fails.
		cmd = exec.CommandContext(ctx, "traceroute6", "-n", "-w", "2", target)
	default:
		cmd = exec.CommandContext(ctx, "traceroute", "-n", "-w", "2", target)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("traceroute pipe: %w", err)
	}
	// Captured so a failure can say why. Windows tracert writes its
	// "cannot resolve" message to stdout, so the hop-count check below covers
	// that case too.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("traceroute start: %w", err)
	}

	var hops []TracerouteHop
	scanner := bufio.NewScanner(stdout)

	// Emitting is the caller's business; the parsers stay pure so they can be
	// run against recorded output of both families.
	if runtime.GOOS == "windows" {
		hops = parseWindowsTraceroute(scanner, emit)
	} else {
		hops = parseUnixTraceroute(scanner, emit)
	}

	// The error, not just the hops. cmd.Wait's result was dropped and stderr
	// was never wired, so a traceroute that started and failed — an unknown
	// name, no permission, no such binary — returned an empty list and a nil
	// error, and the panel showed nothing at all with nothing to explain it.
	waitErr := cmd.Wait()
	if len(hops) == 0 {
		detail := strings.TrimSpace(stderr.String())
		switch {
		case detail != "":
			return nil, fmt.Errorf("traceroute failed: %s", firstLine(detail))
		case waitErr != nil:
			return nil, fmt.Errorf("traceroute failed: %w", waitErr)
		}
	}
	return hops, nil
}

// firstLine keeps an error to one line: these tools print a paragraph.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// parseWindowsTraceroute parses Windows tracert output.
// Format: "  1     1 ms     1 ms     1 ms  192.168.1.1"
func parseWindowsTraceroute(scanner *bufio.Scanner, emit func(TracerouteHop)) []TracerouteHop {
	hopRe := regexp.MustCompile(`^\s*(\d+)\s+(.+)$`)
	rttRe := regexp.MustCompile(`(\d+)\s*ms|\*`)

	var hops []TracerouteHop
	for scanner.Scan() {
		line := scanner.Text()
		m := hopRe.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}

		hopNum, _ := strconv.Atoi(m[1])
		rest := m[2]

		// Extract RTTs
		rttMatches := rttRe.FindAllString(rest, 3)
		rtts := formatRTTs(rttMatches)

		// The last address on the line, in either family.
		ip := netaddr.LastAddressIn(rest)

		hop := TracerouteHop{
			Hop:     hopNum,
			IP:      ip,
			RTT1:    rtts[0],
			RTT2:    rtts[1],
			RTT3:    rtts[2],
			Timeout: rtts[0] == "*" && rtts[1] == "*" && rtts[2] == "*",
		}
		hops = append(hops, hop)
		emit(hop)
	}
	return hops
}

// parseUnixTraceroute parses Linux/macOS traceroute output.
// Format: " 1  192.168.1.1  1.234 ms  0.987 ms  1.123 ms"
func parseUnixTraceroute(scanner *bufio.Scanner, emit func(TracerouteHop)) []TracerouteHop {
	hopRe := regexp.MustCompile(`^\s*(\d+)\s+(.+)$`)
	rttRe := regexp.MustCompile(`([\d.]+)\s*ms|\*`)

	var hops []TracerouteHop
	for scanner.Scan() {
		line := scanner.Text()
		m := hopRe.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}

		hopNum, _ := strconv.Atoi(m[1])
		rest := m[2]

		// The last address on the line, in either family.
		ip := netaddr.LastAddressIn(rest)

		// Extract RTTs
		rttMatches := rttRe.FindAllStringSubmatch(rest, 3)
		rtts := []string{"*", "*", "*"}
		for i, rm := range rttMatches {
			if i >= 3 {
				break
			}
			if len(rm) > 1 && rm[1] != "" {
				rtts[i] = rm[1] + " ms"
			}
		}

		hop := TracerouteHop{
			Hop:     hopNum,
			IP:      ip,
			RTT1:    rtts[0],
			RTT2:    rtts[1],
			RTT3:    rtts[2],
			Timeout: rtts[0] == "*" && rtts[1] == "*" && rtts[2] == "*",
		}
		hops = append(hops, hop)
		emit(hop)
	}
	return hops
}

func formatRTTs(matches []string) [3]string {
	rtts := [3]string{"*", "*", "*"}
	for i, m := range matches {
		if i >= 3 {
			break
		}
		m = strings.TrimSpace(m)
		if m == "*" {
			continue
		}
		// Already contains "ms", keep as-is
		if strings.Contains(m, "ms") {
			rtts[i] = m
		} else {
			rtts[i] = m + " ms"
		}
	}
	return rtts
}

// isIPv6Target reports whether a traceroute should go over IPv6.
//
// Resolved, not just parsed: a hostname with only a AAAA record needs
// traceroute6 on macOS exactly as much as a literal does, and the name gives
// no hint of that.
func isIPv6Target(target string) bool {
	if ip := net.ParseIP(strings.Split(target, "%")[0]); ip != nil {
		return ip.To4() == nil
	}
	addr, err := net.ResolveIPAddr("ip", target)
	return err == nil && addr.IP.To4() == nil
}
