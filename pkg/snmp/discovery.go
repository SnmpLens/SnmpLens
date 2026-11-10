package snmp

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// DiscoveryResult represents the outcome of scanning a single IP.
type DiscoveryResult struct {
	IP      string `json:"ip"`
	SysName string `json:"sysName"`
	// SysObjectID is the vendor and model, as an OID. It is what a dashboard
	// preset matches on: sysDescr is prose that differs between two firmware
	// revisions of the same switch, and this does not.
	SysObjectID  string `json:"sysObjectId,omitempty"`
	SysDescr     string `json:"sysDescr"`
	SysUpTime    string `json:"sysUpTime"`
	ResponseTime int64  `json:"responseTime"`
	Reachable    bool   `json:"reachable"`
	Error        string `json:"error,omitempty"`
}

// Discover scans a CIDR range for SNMP-responsive devices.
func (c *Client) Discover(cidr, community, version string, port, timeoutSec int, v3 V3Params) []DiscoveryResult {
	ips, err := expandCIDR(cidr)
	if err != nil {
		return []DiscoveryResult{{IP: cidr, Error: err.Error()}}
	}

	var wg sync.WaitGroup
	resultsChan := make(chan DiscoveryResult, len(ips))
	sem := make(chan struct{}, 50)
	total := len(ips)

	for i, ip := range ips {
		wg.Add(1)
		go func(ipAddr string, index int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			result := DiscoveryResult{IP: ipAddr}

			g, err := c.newGoSNMP(ipAddr, community, version, port, timeoutSec, 0, v3)
			if err != nil {
				result.Error = err.Error()
				result.ResponseTime = time.Since(start).Milliseconds()
				resultsChan <- result
				c.discoveryProgress(index+1, total, ipAddr)
				return
			}
			if err = g.Connect(); err != nil {
				result.Error = fmt.Sprintf("connect failed: %v", err)
				result.ResponseTime = time.Since(start).Milliseconds()
				resultsChan <- result
				c.discoveryProgress(index+1, total, ipAddr)
				return
			}
			defer g.Conn.Close()

			oids := []string{
				".1.3.6.1.2.1.1.1.0", // sysDescr
				".1.3.6.1.2.1.1.5.0", // sysName
				".1.3.6.1.2.1.1.3.0", // sysUpTime
				".1.3.6.1.2.1.1.2.0", // sysObjectID
			}
			packet, err := g.Get(oids)
			result.ResponseTime = time.Since(start).Milliseconds()

			if err != nil {
				result.Error = fmt.Sprintf("get failed: %v", err)
				resultsChan <- result
				c.discoveryProgress(index+1, total, ipAddr)
				return
			}

			result.Reachable = true
			for _, v := range packet.Variables {
				val := formatSnmpValue(v)
				switch v.Name {
				case ".1.3.6.1.2.1.1.1.0":
					result.SysDescr = fmt.Sprintf("%v", val)
				case ".1.3.6.1.2.1.1.5.0":
					result.SysName = fmt.Sprintf("%v", val)
				case ".1.3.6.1.2.1.1.3.0":
					result.SysUpTime = fmt.Sprintf("%v", val)
				case ".1.3.6.1.2.1.1.2.0":
					result.SysObjectID = fmt.Sprintf("%v", val)
				}
			}
			resultsChan <- result
			c.discoveryProgress(index+1, total, ipAddr)
		}(ip, i)
	}

	wg.Wait()
	close(resultsChan)

	results := make([]DiscoveryResult, 0, len(ips))
	for r := range resultsChan {
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		return compareIPs(results[i].IP, results[j].IP)
	})
	return results
}

// discoveryProgress tells a window how far a scan has got. Only a window: the
// Wails runtime refuses a context it did not issue and ends the process, so a
// client without one — a test, a headless run — scans without saying so, as
// handleTrap receives without emitting.
func (c *Client) discoveryProgress(current, total int, ip string) {
	if c.ctx != nil || c.emitter != nil {
		c.emit("discoveryProgress", map[string]interface{}{"current": current, "total": total, "ip": ip})
	}
}

// MaxDiscoveryHosts caps a scan.
//
// Not a preference. expandCIDR materialises every address into a slice and
// Discover allocates one channel slot per address, so the prefix decides the
// allocation before a single packet is sent: 10.0.0.0/8 is 16.7 million
// strings, and an IPv6 /64 is 1.8e19 — the loop never terminates and the
// process dies with nothing on screen to say why. Nothing stopped either from
// being typed.
const MaxDiscoveryHosts = 65536

func expandCIDR(cidr string) ([]string, error) {
	if ip := net.ParseIP(strings.TrimSpace(cidr)); ip != nil {
		return []string{ip.String()}, nil
	}

	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR or IP: %s", cidr)
	}

	// Counted from the prefix, never by walking: walking is the thing that
	// cannot be allowed to start.
	ones, bits := ipNet.Mask.Size()
	if bits == 0 {
		return nil, fmt.Errorf("invalid netmask in %s", cidr)
	}
	hostBits := bits - ones
	if hostBits > 31 || uint64(1)<<uint(hostBits) > MaxDiscoveryHosts {
		return nil, fmt.Errorf(
			"%s covers more than %d addresses; scan a smaller prefix (/%d or longer)",
			cidr, MaxDiscoveryHosts, bits-16)
	}

	ips := make([]string, 0, uint64(1)<<uint(hostBits))
	for ip := ip.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
		ips = append(ips, ip.String())
	}

	// The first and last addresses of an IPv4 subnet are the network and the
	// broadcast, and neither answers SNMP. IPv6 has no broadcast and no
	// reserved subnet-router address a host cannot also hold, so dropping them
	// there just skips two real hosts — 2001:db8::/126 lost ::0 and ::3.
	if ipNet.IP.To4() != nil && len(ips) > 2 {
		ips = ips[1 : len(ips)-1]
	}
	return ips, nil
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func compareIPs(a, b string) bool {
	ipA := net.ParseIP(a)
	ipB := net.ParseIP(b)
	if ipA == nil || ipB == nil {
		return a < b
	}
	return string(ipA.To16()) < string(ipB.To16())
}
