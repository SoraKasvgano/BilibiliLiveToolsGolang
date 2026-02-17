package onvif

import (
	"bytes"
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Service struct {
	client *http.Client
}

type CommandRequest struct {
	Endpoint     string  `json:"endpoint"`
	Username     string  `json:"username"`
	Password     string  `json:"password"`
	ProfileToken string  `json:"profileToken"`
	Action       string  `json:"action"`
	Direction    string  `json:"direction"`
	Speed        float64 `json:"speed"`
	DurationMS   int     `json:"durationMs"`
	Pan          float64 `json:"pan"`
	Tilt         float64 `json:"tilt"`
	Zoom         float64 `json:"zoom"`
	PresetToken  string  `json:"presetToken"`
}

type Capabilities struct {
	MediaXAddr string `json:"mediaXAddr"`
	PTZXAddr   string `json:"ptzXAddr"`
}

type Profile struct {
	Token     string `json:"token"`
	Name      string `json:"name,omitempty"`
	RTSPURL   string `json:"rtspUrl,omitempty"`
	Preferred bool   `json:"preferred,omitempty"`
}

type DiscoveredDevice struct {
	Endpoint string   `json:"endpoint"`
	XAddrs   []string `json:"xAddrs"`
	URN      string   `json:"urn"`
	Types    []string `json:"types"`
	Scopes   []string `json:"scopes"`
	From     string   `json:"from"`
}

type DiscoverOptions struct {
	Timeout        time.Duration
	ActiveScan     bool
	Ports          []int
	MaxHosts       int
	MaxConcurrency int
	RequestTimeout time.Duration
}

var defaultDiscoverPorts = []int{80, 2020, 554, 8080, 8000, 8899, 443, 8443}

func New() *Service {
	return &Service{
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (s *Service) Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	return s.DiscoverWithOptions(ctx, DiscoverOptions{
		Timeout:        timeout,
		ActiveScan:     true,
		Ports:          defaultDiscoverPorts,
		MaxHosts:       512,
		MaxConcurrency: 48,
		RequestTimeout: 700 * time.Millisecond,
	})
}

func (s *Service) DiscoverWithOptions(ctx context.Context, opts DiscoverOptions) ([]DiscoveredDevice, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if timeout > 15*time.Second {
		timeout = 15 * time.Second
	}
	ports := normalizeDiscoveryPorts(opts.Ports)
	if len(ports) == 0 {
		ports = append([]int{}, defaultDiscoverPorts...)
	}
	maxHosts := opts.MaxHosts
	if maxHosts <= 0 {
		maxHosts = 512
	}
	if maxHosts > 4096 {
		maxHosts = 4096
	}
	maxConcurrency := opts.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 48
	}
	if maxConcurrency > 256 {
		maxConcurrency = 256
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 700 * time.Millisecond
	}
	if requestTimeout > 3*time.Second {
		requestTimeout = 3 * time.Second
	}
	startedAt := time.Now()
	wsTimeout := timeout
	if opts.ActiveScan && timeout > 2200*time.Millisecond {
		wsTimeout = (timeout * 2) / 3
		if wsTimeout < 1200*time.Millisecond {
			wsTimeout = 1200 * time.Millisecond
		}
	}

	bindIPs := discoverCandidateIPv4()
	targets := make([]net.IP, 0, len(bindIPs)+1)
	targets = append(targets, nil) // wildcard socket for default route
	targets = append(targets, bindIPs...)

	probes := make([]string, 0, 2)
	for _, types := range []string{"dn:NetworkVideoTransmitter", ""} {
		messageID, err := randomUUID()
		if err != nil {
			return nil, err
		}
		probes = append(probes, buildDiscoveryProbe("urn:uuid:"+messageID, types))
	}

	type discoverResult struct {
		items []DiscoveredDevice
		err   error
	}
	results := make(chan discoverResult, len(targets))

	var wg sync.WaitGroup
	for _, bindIP := range targets {
		ip := append(net.IP(nil), bindIP...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := s.discoverFromBind(ctx, ip, wsTimeout, probes)
			results <- discoverResult{items: items, err: err}
		}()
	}
	wg.Wait()
	close(results)

	seen := map[string]DiscoveredDevice{}
	var firstErr error
	successCount := 0
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		successCount++
		for _, device := range result.items {
			mergeDiscoveredDevice(seen, device)
		}
	}
	if successCount == 0 && firstErr != nil {
		return nil, firstErr
	}

	remaining := timeout - time.Since(startedAt)
	if opts.ActiveScan && remaining > 1200*time.Millisecond {
		activeItems := s.activeDiscover(ctx, remaining, ports, maxHosts, maxConcurrency, requestTimeout)
		for _, item := range activeItems {
			mergeDiscoveredDevice(seen, item)
		}
	}

	result := make([]DiscoveredDevice, 0, len(seen))
	for _, item := range seen {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Endpoint == result[j].Endpoint {
			return result[i].URN < result[j].URN
		}
		return result[i].Endpoint < result[j].Endpoint
	})
	return result, nil
}

func (s *Service) discoverFromBind(ctx context.Context, bindIP net.IP, timeout time.Duration, probes []string) ([]DiscoveredDevice, error) {
	listenAddr := ":0"
	if ip := strings.TrimSpace(bindIP.String()); ip != "" && ip != "<nil>" {
		listenAddr = net.JoinHostPort(ip, "0")
	}
	conn, err := net.ListenPacket("udp4", listenAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	target := &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 3702}
	for attempt := 0; attempt < 2; attempt++ {
		for _, probe := range probes {
			if _, err := conn.WriteTo([]byte(probe), target); err != nil {
				return nil, err
			}
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(120 * time.Millisecond):
			}
		}
	}

	seen := map[string]DiscoveredDevice{}
	buffer := make([]byte, 64<<10)
	deadline := time.Now().Add(timeout)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		wait := 350 * time.Millisecond
		if remain < wait {
			wait = remain
		}
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		n, remoteAddr, readErr := conn.ReadFrom(buffer)
		if readErr != nil {
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return nil, readErr
		}
		device := parseDiscoveryResponse(string(buffer[:n]))
		if len(device.XAddrs) == 0 {
			continue
		}
		if !isLikelyONVIFDevice(device) {
			continue
		}
		device.From = remoteAddr.String()
		mergeDiscoveredDevice(seen, device)
	}

	items := make([]DiscoveredDevice, 0, len(seen))
	for _, item := range seen {
		items = append(items, item)
	}
	return items, nil
}

func discoverCandidateIPv4() []net.IP {
	items, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	result := make([]net.IP, 0, 4)
	for _, iface := range items {
		// Exclude down/loopback interfaces; keep interface scan lightweight.
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addressIPv4(addr)
			if ip == nil {
				continue
			}
			key := ip.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, ip)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func addressIPv4(addr net.Addr) net.IP {
	var ip net.IP
	switch value := addr.(type) {
	case *net.IPNet:
		ip = value.IP
	case *net.IPAddr:
		ip = value.IP
	default:
		return nil
	}
	ip = ip.To4()
	if ip == nil {
		return nil
	}
	if ip.IsLoopback() || ip.IsLinkLocalMulticast() {
		return nil
	}
	return append(net.IP(nil), ip...)
}

func mergeDiscoveredDevice(seen map[string]DiscoveredDevice, device DiscoveredDevice) {
	if seen == nil {
		return
	}
	if strings.TrimSpace(device.Endpoint) == "" && len(device.XAddrs) > 0 {
		device.Endpoint = strings.TrimSpace(device.XAddrs[0])
	}
	key := strings.TrimSpace(device.URN)
	if key == "" {
		key = strings.TrimSpace(device.Endpoint)
	}
	if key == "" {
		return
	}
	if existing, ok := seen[key]; ok {
		existing.XAddrs = mergeStringSlice(existing.XAddrs, device.XAddrs)
		existing.Types = mergeStringSlice(existing.Types, device.Types)
		existing.Scopes = mergeStringSlice(existing.Scopes, device.Scopes)
		if existing.Endpoint == "" && device.Endpoint != "" {
			existing.Endpoint = device.Endpoint
		}
		if existing.From == "" && device.From != "" {
			existing.From = device.From
		}
		seen[key] = existing
		return
	}
	seen[key] = device
}

func isLikelyONVIFDevice(device DiscoveredDevice) bool {
	for _, xaddr := range device.XAddrs {
		if strings.Contains(strings.ToLower(strings.TrimSpace(xaddr)), "/onvif") {
			return true
		}
	}
	for _, item := range device.Types {
		value := strings.ToLower(strings.TrimSpace(item))
		if strings.Contains(value, "networkvideotransmitter") ||
			strings.Contains(value, "tds:device") ||
			strings.Contains(value, "video_encoder") ||
			strings.Contains(value, "network_video") {
			return true
		}
	}
	for _, scope := range device.Scopes {
		if strings.Contains(strings.ToLower(strings.TrimSpace(scope)), "onvif://") {
			return true
		}
	}
	return false
}

func normalizeDiscoveryPorts(ports []int) []int {
	if len(ports) == 0 {
		return nil
	}
	seen := map[int]struct{}{}
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		result = append(result, port)
	}
	return result
}

func (s *Service) activeDiscover(parent context.Context, timeout time.Duration, ports []int, maxHosts int, maxConcurrency int, requestTimeout time.Duration) []DiscoveredDevice {
	if timeout < 2*time.Second {
		return nil
	}
	if len(ports) == 0 || maxHosts <= 0 || maxConcurrency <= 0 {
		return nil
	}
	ctx := parent
	var cancel context.CancelFunc
	if deadline, ok := parent.Deadline(); !ok || time.Until(deadline) > timeout {
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}

	hosts := discoverSubnetHosts(maxHosts)
	if len(hosts) == 0 {
		return nil
	}

	results := make(chan DiscoveredDevice, len(hosts))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, host := range hosts {
		hostIP := append(net.IP(nil), host...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			device, ok := s.probeHostForONVIF(ctx, hostIP, ports, requestTimeout)
			if !ok {
				return
			}
			select {
			case <-ctx.Done():
				return
			case results <- device:
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := map[string]DiscoveredDevice{}
	for item := range results {
		mergeDiscoveredDevice(seen, item)
	}
	items := make([]DiscoveredDevice, 0, len(seen))
	for _, item := range seen {
		items = append(items, item)
	}
	return items
}

func (s *Service) probeHostForONVIF(ctx context.Context, host net.IP, ports []int, requestTimeout time.Duration) (DiscoveredDevice, bool) {
	if host == nil {
		return DiscoveredDevice{}, false
	}
	paths := []string{
		"/onvif/device_service",
		"/onvif/device_service/",
		"/onvif/deviceservice",
		"/onvif/device_service?wsdl",
	}
	hostText := host.String()
	for _, port := range ports {
		for _, scheme := range discoverySchemesForPort(port) {
			for _, requestPath := range paths {
				select {
				case <-ctx.Done():
					return DiscoveredDevice{}, false
				default:
				}
				endpoint := fmt.Sprintf("%s://%s:%d%s", scheme, hostText, port, requestPath)
				if !s.probeONVIFEndpoint(ctx, endpoint, requestTimeout) {
					continue
				}
				return DiscoveredDevice{
					Endpoint: endpoint,
					XAddrs:   []string{endpoint},
					Types:    []string{"dn:NetworkVideoTransmitter"},
					Scopes:   []string{"scan://active-probe"},
					From:     "active-probe",
				}, true
			}
		}
	}
	return DiscoveredDevice{}, false
}

func discoverySchemesForPort(port int) []string {
	if port == 443 || port == 8443 {
		return []string{"https", "http"}
	}
	return []string{"http", "https"}
}

func (s *Service) probeONVIFEndpoint(parent context.Context, endpoint string, requestTimeout time.Duration) bool {
	probeCtx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()

	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		`<s:Body>` +
		`<tds:GetCapabilities xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
		`<tds:Category>All</tds:Category>` +
		`</tds:GetCapabilities>` +
		`</s:Body></s:Envelope>`

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, endpoint, strings.NewReader(envelope))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8; action="http://www.onvif.org/ver10/device/wsdl/GetCapabilities"`)
	req.Header.Set("SOAPAction", "http://www.onvif.org/ver10/device/wsdl/GetCapabilities")
	req.Header.Set("User-Agent", "gover-onvif-discover/1.0")

	resp, err := s.client.Do(req)
	if err != nil || resp == nil {
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return isLikelyONVIFHTTPResponse(endpoint, resp.StatusCode, resp.Header, body)
}

func isLikelyONVIFHTTPResponse(endpoint string, statusCode int, headers http.Header, body []byte) bool {
	if statusCode == http.StatusNotFound {
		return false
	}
	lowerEndpoint := strings.ToLower(strings.TrimSpace(endpoint))
	lowerBody := strings.ToLower(string(body))
	lowerType := strings.ToLower(strings.TrimSpace(headers.Get("Content-Type")))
	lowerAuth := strings.ToLower(strings.TrimSpace(headers.Get("WWW-Authenticate")))

	if strings.Contains(lowerBody, "onvif") ||
		strings.Contains(lowerBody, "soap") ||
		strings.Contains(lowerBody, "envelope") ||
		strings.Contains(lowerType, "soap") ||
		strings.Contains(lowerType, "xml") {
		return true
	}
	if strings.Contains(lowerAuth, "digest") || strings.Contains(lowerAuth, "basic") {
		if strings.Contains(lowerEndpoint, "/onvif/") {
			return true
		}
	}
	if (statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusMethodNotAllowed) && strings.Contains(lowerEndpoint, "/onvif/") {
		return true
	}
	return false
}

func discoverSubnetHosts(maxHosts int) []net.IP {
	if maxHosts <= 0 {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	hosts := make([]net.IP, 0, maxHosts)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet == nil {
				continue
			}
			localIP := ipNet.IP.To4()
			if localIP == nil {
				continue
			}
			candidates := expandHostCandidates(localIP, ipNet.Mask)
			for _, host := range candidates {
				key := host.String()
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				hosts = append(hosts, host)
				if len(hosts) >= maxHosts {
					sort.Slice(hosts, func(i, j int) bool { return hosts[i].String() < hosts[j].String() })
					return hosts
				}
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].String() < hosts[j].String() })
	return hosts
}

func expandHostCandidates(localIP net.IP, mask net.IPMask) []net.IP {
	ip := localIP.To4()
	if ip == nil {
		return nil
	}
	ones, bits := mask.Size()
	if bits != 32 {
		return nil
	}
	if ones < 24 {
		mask = net.CIDRMask(24, 32)
		ones = 24
	}
	if ones >= 31 {
		return nil
	}
	network := ip.Mask(mask).To4()
	if network == nil {
		return nil
	}
	total := 1 << uint(32-ones)
	start := ipToUint32(network) + 1
	end := ipToUint32(network) + uint32(total) - 2
	if end < start {
		return nil
	}

	result := make([]net.IP, 0, total-2)
	localValue := ipToUint32(ip)
	for value := start; value <= end; value++ {
		if value == localValue {
			continue
		}
		result = append(result, uint32ToIPv4(value))
	}
	return result
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	if v == nil {
		return 0
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uint32ToIPv4(value uint32) net.IP {
	return net.IPv4(
		byte((value>>24)&0xff),
		byte((value>>16)&0xff),
		byte((value>>8)&0xff),
		byte(value&0xff),
	).To4()
}

func (s *Service) GetCapabilities(ctx context.Context, endpoint string, username string, password string) (*Capabilities, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("onvif endpoint is required")
	}
	env := `<tds:GetCapabilities xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Category>All</tds:Category></tds:GetCapabilities>`
	body, err := s.callSOAP(ctx, endpoint, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities", env, username, password)
	if err != nil {
		return nil, err
	}
	caps := &Capabilities{
		MediaXAddr: extractXAddrByPath(body, []string{"Capabilities", "Media", "XAddr"}),
		PTZXAddr:   extractXAddrByPath(body, []string{"Capabilities", "PTZ", "XAddr"}),
	}
	if caps.MediaXAddr == "" {
		caps.MediaXAddr = extractXAddrByPath(body, []string{"Capabilities", "Media", "XAddr"})
	}
	if caps.MediaXAddr == "" {
		caps.MediaXAddr = endpoint
	}
	if caps.PTZXAddr == "" {
		caps.PTZXAddr = endpoint
	}
	return caps, nil
}

func (s *Service) GetProfiles(ctx context.Context, endpoint string, username string, password string) ([]Profile, error) {
	caps, err := s.GetCapabilities(ctx, endpoint, username, password)
	if err != nil {
		return nil, err
	}
	env := `<trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/>`
	body, err := s.callSOAP(ctx, caps.MediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetProfiles", env, username, password)
	if err != nil {
		return nil, err
	}
	profiles := parseProfilesFromResponse(body)
	if len(profiles) == 0 {
		return nil, errors.New("no onvif profiles found")
	}
	for idx := range profiles {
		streamURL, streamErr := s.getProfileStreamURI(ctx, caps.MediaXAddr, profiles[idx].Token, username, password)
		if streamErr == nil && streamURL != "" {
			profiles[idx].RTSPURL = streamURL
		}
	}
	setPreferredProfile(profiles)
	return profiles, nil
}

func parseProfilesFromResponse(body string) []Profile {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	decoder := xml.NewDecoder(strings.NewReader(body))
	type profileNode struct {
		Token string `xml:"token,attr"`
		Name  string `xml:"Name"`
	}
	uniq := map[string]struct{}{}
	result := make([]Profile, 0, 8)
	for {
		tok, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return result
		}
		start, ok := tok.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "Profiles") {
			continue
		}
		node := profileNode{}
		if err := decoder.DecodeElement(&node, &start); err != nil {
			continue
		}
		token := strings.TrimSpace(node.Token)
		if token == "" {
			continue
		}
		if _, exists := uniq[token]; exists {
			continue
		}
		uniq[token] = struct{}{}
		result = append(result, Profile{
			Token: token,
			Name:  strings.TrimSpace(node.Name),
		})
	}
	if len(result) > 0 {
		return result
	}
	// Fallback for non-standard XML layout.
	matches := regexp.MustCompile(`token="([^"]+)"`).FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		token := strings.TrimSpace(m[1])
		if token == "" {
			continue
		}
		if _, exists := uniq[token]; exists {
			continue
		}
		uniq[token] = struct{}{}
		result = append(result, Profile{Token: token})
	}
	return result
}

func setPreferredProfile(items []Profile) {
	for idx := range items {
		if strings.TrimSpace(items[idx].RTSPURL) == "" {
			continue
		}
		items[idx].Preferred = true
		return
	}
	if len(items) > 0 {
		items[0].Preferred = true
	}
}

func (s *Service) getProfileStreamURI(ctx context.Context, mediaXAddr string, profileToken string, username string, password string) (string, error) {
	profileToken = strings.TrimSpace(profileToken)
	if profileToken == "" {
		return "", errors.New("profile token is required")
	}
	env := fmt.Sprintf(
		`<trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">`+
			`<trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup>`+
			`<trt:ProfileToken>%s</trt:ProfileToken></trt:GetStreamUri>`,
		xmlEscape(profileToken),
	)
	body, err := s.callSOAP(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetStreamUri", env, username, password)
	if err != nil {
		return "", err
	}
	streamURI := strings.TrimSpace(firstTagValue(body, "Uri"))
	if streamURI == "" {
		streamURI = strings.TrimSpace(firstTagValue(body, "URI"))
	}
	if streamURI == "" {
		return "", errors.New("stream uri not found")
	}
	return injectRTSPAuth(streamURI, username, password), nil
}

func injectRTSPAuth(rawURL string, username string, password string) string {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return value
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Scheme), "rtsp") {
		return value
	}
	if parsed.User != nil && strings.TrimSpace(parsed.User.Username()) != "" {
		return value
	}
	if strings.TrimSpace(password) == "" {
		parsed.User = url.User(username)
	} else {
		parsed.User = url.UserPassword(username, password)
	}
	return parsed.String()
}

func (s *Service) ExecuteCommand(ctx context.Context, req CommandRequest) (map[string]any, error) {
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Username = strings.TrimSpace(req.Username)
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	req.Direction = strings.ToLower(strings.TrimSpace(req.Direction))
	if req.Endpoint == "" {
		return nil, errors.New("onvif endpoint is required")
	}
	if req.Action == "" {
		if req.Direction != "" {
			req.Action = req.Direction
		} else {
			req.Action = "stop"
		}
	}
	if req.Speed <= 0 {
		req.Speed = 0.3
	}
	if req.Speed > 1 {
		req.Speed = 1
	}
	if req.DurationMS <= 0 {
		req.DurationMS = 700
	}
	if req.DurationMS > 10000 {
		req.DurationMS = 10000
	}

	caps, err := s.GetCapabilities(ctx, req.Endpoint, req.Username, req.Password)
	if err != nil {
		return nil, err
	}
	if req.ProfileToken == "" {
		profiles, profileErr := s.GetProfiles(ctx, req.Endpoint, req.Username, req.Password)
		if profileErr != nil {
			return nil, profileErr
		}
		req.ProfileToken = profiles[0].Token
	}

	action := req.Action
	resultData := map[string]any{
		"ok":           true,
		"action":       action,
		"profileToken": req.ProfileToken,
		"ptzXAddr":     caps.PTZXAddr,
		"mediaXAddr":   caps.MediaXAddr,
	}
	switch action {
	case "left", "right", "up", "down", "zoom_in", "zoom_out":
		pan, tilt, zoom := velocityByAction(action, req.Speed)
		err = s.continuousMove(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, pan, tilt, zoom)
		if err != nil {
			return nil, err
		}
		time.Sleep(time.Duration(req.DurationMS) * time.Millisecond)
		err = s.stop(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken)
		if err != nil {
			return nil, err
		}
	case "stop":
		err = s.stop(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken)
		if err != nil {
			return nil, err
		}
	case "home":
		err = s.gotoHome(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken)
		if err != nil {
			return nil, err
		}
	case "absolute":
		err = s.absoluteMove(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, req.Pan, req.Tilt, req.Zoom)
		if err != nil {
			return nil, err
		}
	case "relative":
		err = s.relativeMove(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, req.Pan, req.Tilt, req.Zoom)
		if err != nil {
			return nil, err
		}
	case "status":
		status, statusErr := s.getStatus(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken)
		if statusErr != nil {
			return nil, statusErr
		}
		resultData["status"] = status
	case "goto_preset", "preset_goto":
		if strings.TrimSpace(req.PresetToken) == "" {
			return nil, errors.New("preset token is required for goto_preset")
		}
		err = s.gotoPreset(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, req.PresetToken, req.Speed)
		if err != nil {
			return nil, err
		}
		resultData["presetToken"] = req.PresetToken
	case "set_preset", "preset_set":
		token, setErr := s.setPreset(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, req.PresetToken)
		if setErr != nil {
			return nil, setErr
		}
		if token != "" {
			req.PresetToken = token
		}
		resultData["presetToken"] = req.PresetToken
	case "remove_preset", "preset_remove":
		if strings.TrimSpace(req.PresetToken) == "" {
			return nil, errors.New("preset token is required for remove_preset")
		}
		err = s.removePreset(ctx, caps.PTZXAddr, req.Username, req.Password, req.ProfileToken, req.PresetToken)
		if err != nil {
			return nil, err
		}
		resultData["presetToken"] = req.PresetToken
	default:
		return nil, fmt.Errorf("unsupported ptz action: %s", action)
	}

	return resultData, nil
}

func (s *Service) continuousMove(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, pan float64, tilt float64, zoom float64) error {
	env := fmt.Sprintf(`<tptz:ContinuousMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="%.3f" y="%.3f"/><tt:Zoom x="%.3f"/></tptz:Velocity></tptz:ContinuousMove>`, xmlEscape(profileToken), pan, tilt, zoom)
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/ContinuousMove", env, username, password)
	return err
}

func (s *Service) stop(ctx context.Context, ptzXAddr string, username string, password string, profileToken string) error {
	env := fmt.Sprintf(`<tptz:Stop xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`, xmlEscape(profileToken))
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/Stop", env, username, password)
	return err
}

func (s *Service) gotoHome(ctx context.Context, ptzXAddr string, username string, password string, profileToken string) error {
	env := fmt.Sprintf(`<tptz:GotoHomePosition xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GotoHomePosition>`, xmlEscape(profileToken))
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/GotoHomePosition", env, username, password)
	return err
}

func (s *Service) absoluteMove(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, pan float64, tilt float64, zoom float64) error {
	env := fmt.Sprintf(`<tptz:AbsoluteMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Position><tt:PanTilt x="%.3f" y="%.3f"/><tt:Zoom x="%.3f"/></tptz:Position></tptz:AbsoluteMove>`, xmlEscape(profileToken), pan, tilt, zoom)
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/AbsoluteMove", env, username, password)
	return err
}

func (s *Service) relativeMove(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, pan float64, tilt float64, zoom float64) error {
	env := fmt.Sprintf(`<tptz:RelativeMove xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Translation><tt:PanTilt x="%.3f" y="%.3f"/><tt:Zoom x="%.3f"/></tptz:Translation></tptz:RelativeMove>`, xmlEscape(profileToken), pan, tilt, zoom)
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/RelativeMove", env, username, password)
	return err
}

func (s *Service) gotoPreset(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, presetToken string, speed float64) error {
	velocity := ""
	if speed > 0 {
		velocity = fmt.Sprintf(`<tptz:Speed><tt:PanTilt x="%.3f" y="%.3f"/><tt:Zoom x="%.3f"/></tptz:Speed>`, speed, speed, speed)
	}
	env := fmt.Sprintf(`<tptz:GotoPreset xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken>%s</tptz:GotoPreset>`, xmlEscape(profileToken), xmlEscape(presetToken), velocity)
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/GotoPreset", env, username, password)
	return err
}

func (s *Service) setPreset(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, presetToken string) (string, error) {
	presetField := ""
	if strings.TrimSpace(presetToken) != "" {
		presetField = fmt.Sprintf(`<tptz:PresetToken>%s</tptz:PresetToken>`, xmlEscape(presetToken))
	}
	env := fmt.Sprintf(`<tptz:SetPreset xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken>%s</tptz:SetPreset>`, xmlEscape(profileToken), presetField)
	body, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/SetPreset", env, username, password)
	if err != nil {
		return "", err
	}
	token := firstTagValue(body, "PresetToken")
	if token == "" {
		token = firstTagValue(body, "presetToken")
	}
	return strings.TrimSpace(token), nil
}

func (s *Service) removePreset(ctx context.Context, ptzXAddr string, username string, password string, profileToken string, presetToken string) error {
	env := fmt.Sprintf(`<tptz:RemovePreset xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken></tptz:RemovePreset>`, xmlEscape(profileToken), xmlEscape(presetToken))
	_, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/RemovePreset", env, username, password)
	return err
}

func (s *Service) getStatus(ctx context.Context, ptzXAddr string, username string, password string, profileToken string) (map[string]any, error) {
	env := fmt.Sprintf(`<tptz:GetStatus xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetStatus>`, xmlEscape(profileToken))
	body, err := s.callSOAP(ctx, ptzXAddr, "http://www.onvif.org/ver20/ptz/wsdl/GetStatus", env, username, password)
	if err != nil {
		return nil, err
	}
	status := map[string]any{
		"raw": body,
	}
	if pan, ok := extractXMLAttributeFloat(body, "PanTilt", "x"); ok {
		status["pan"] = pan
	}
	if tilt, ok := extractXMLAttributeFloat(body, "PanTilt", "y"); ok {
		status["tilt"] = tilt
	}
	if zoom, ok := extractXMLAttributeFloat(body, "Zoom", "x"); ok {
		status["zoom"] = zoom
	}
	if move := strings.TrimSpace(firstTagValue(body, "MoveStatus")); move != "" {
		status["moveStatus"] = move
	}
	if utc := strings.TrimSpace(firstTagValue(body, "UtcTime")); utc != "" {
		status["utcTime"] = utc
	}
	return status, nil
}

func (s *Service) callSOAP(ctx context.Context, endpoint string, action string, body string, username string, password string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		`<s:Body>` + body + `</s:Body></s:Envelope>`

	result, resp, err := s.doSOAPRequest(ctx, endpoint, action, envelope, "")
	if err == nil {
		return result, nil
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return "", err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "digest") {
		return "", err
	}
	authHeader, authErr := buildDigestHeader(challenge, username, password, http.MethodPost, endpoint)
	if authErr != nil {
		return "", authErr
	}
	result, _, err = s.doSOAPRequest(ctx, endpoint, action, envelope, authHeader)
	if err != nil {
		return "", err
	}
	return result, nil
}

func (s *Service) doSOAPRequest(ctx context.Context, endpoint string, action string, envelope string, authorization string) (string, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(envelope))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", fmt.Sprintf("application/soap+xml; charset=utf-8; action=\"%s\"", action))
	req.Header.Set("SOAPAction", action)
	req.Header.Set("User-Agent", "gover-onvif/1.0")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return "", resp, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp, fmt.Errorf("onvif http status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	if bytes.Contains(bytes.ToLower(bodyBytes), []byte("fault")) {
		return "", resp, fmt.Errorf("onvif soap fault: %s", string(bodyBytes))
	}
	return string(bodyBytes), resp, nil
}

func extractXAddrByPath(xmlBody string, pathParts []string) string {
	decoder := xml.NewDecoder(strings.NewReader(xmlBody))
	stack := make([]string, 0, 16)
	for {
		tok, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ""
		}
		switch element := tok.(type) {
		case xml.StartElement:
			stack = append(stack, element.Name.Local)
			if matchPathSuffix(stack, pathParts) {
				var value string
				if err := decoder.DecodeElement(&value, &element); err == nil {
					return strings.TrimSpace(value)
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return ""
}

func matchPathSuffix(stack []string, target []string) bool {
	if len(target) == 0 || len(stack) < len(target) {
		return false
	}
	offset := len(stack) - len(target)
	for i := range target {
		if !strings.EqualFold(stack[offset+i], target[i]) {
			return false
		}
	}
	return true
}

func velocityByAction(action string, speed float64) (float64, float64, float64) {
	switch action {
	case "left":
		return -speed, 0, 0
	case "right":
		return speed, 0, 0
	case "up":
		return 0, speed, 0
	case "down":
		return 0, -speed, 0
	case "zoom_in":
		return 0, 0, speed
	case "zoom_out":
		return 0, 0, -speed
	default:
		return 0, 0, 0
	}
}

func buildDigestHeader(challenge string, username string, password string, method string, rawURL string) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "", errors.New("onvif digest auth username is required")
	}
	params := parseDigestChallenge(challenge)
	realm := params["realm"]
	nonce := params["nonce"]
	if realm == "" || nonce == "" {
		return "", errors.New("invalid digest challenge")
	}
	qop := "auth"
	if value := params["qop"]; value != "" {
		if strings.Contains(value, "auth") {
			qop = "auth"
		} else {
			qop = value
		}
	}
	uriValue, err := digestURI(rawURL)
	if err != nil {
		return "", err
	}
	nc := "00000001"
	cnonce, err := randomHex(8)
	if err != nil {
		return "", err
	}

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uriValue)

	algorithm := params["algorithm"]
	if algorithm == "" {
		algorithm = "MD5"
	}
	if strings.EqualFold(algorithm, "MD5-sess") {
		ha1 = md5Hex(ha1 + ":" + nonce + ":" + cnonce)
	}

	response := ""
	if qop != "" {
		response = md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	parts := []string{
		`Digest username="` + username + `"`,
		`realm="` + realm + `"`,
		`nonce="` + nonce + `"`,
		`uri="` + uriValue + `"`,
		`response="` + response + `"`,
		`algorithm=` + algorithm,
	}
	if opaque := params["opaque"]; opaque != "" {
		parts = append(parts, `opaque="`+opaque+`"`)
	}
	if qop != "" {
		parts = append(parts,
			`qop=`+qop,
			`nc=`+nc,
			`cnonce="`+cnonce+`"`,
		)
	}
	return strings.Join(parts, ", "), nil
}

func parseDigestChallenge(header string) map[string]string {
	result := map[string]string{}
	header = strings.TrimSpace(header)
	if header == "" {
		return result
	}
	if strings.HasPrefix(strings.ToLower(header), "digest") {
		header = strings.TrimSpace(header[len("Digest"):])
	}
	matcher := regexp.MustCompile(`([a-zA-Z]+)=(("[^"]*")|([^,]+))`)
	matches := matcher.FindAllStringSubmatch(header, -1)
	for _, item := range matches {
		if len(item) < 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(item[1]))
		value := strings.TrimSpace(item[2])
		value = strings.Trim(value, `"`)
		result[key] = value
	}
	return result
}

func digestURI(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	uri := parsed.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	if parsed.RawQuery != "" {
		uri += "?" + parsed.RawQuery
	}
	return uri, nil
}

func md5Hex(content string) string {
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

func randomUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4],
		buf[4:6],
		buf[6:8],
		buf[8:10],
		buf[10:16],
	), nil
}

func randomHex(size int) (string, error) {
	if size <= 0 {
		size = 8
	}
	buf := make([]byte, size)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func buildDiscoveryProbe(messageID string, types string) string {
	probeTypes := ""
	if strings.TrimSpace(types) != "" {
		probeTypes = `<d:Types>` + xmlEscape(strings.TrimSpace(types)) + `</d:Types>`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope"` +
		` xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
		` xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"` +
		` xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<e:Header>` +
		`<w:MessageID>` + xmlEscape(messageID) + `</w:MessageID>` +
		`<w:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>` +
		`<w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action>` +
		`</e:Header>` +
		`<e:Body><d:Probe>` + probeTypes + `</d:Probe></e:Body>` +
		`</e:Envelope>`
}

func parseDiscoveryResponse(xmlBody string) DiscoveredDevice {
	xaddrs := splitSpaceSeparated(firstTagValue(xmlBody, "XAddrs"))
	endpoint := ""
	if len(xaddrs) > 0 {
		endpoint = strings.TrimSpace(xaddrs[0])
	}
	return DiscoveredDevice{
		Endpoint: endpoint,
		XAddrs:   xaddrs,
		URN:      firstTagValue(xmlBody, "Address"),
		Types:    splitSpaceSeparated(firstTagValue(xmlBody, "Types")),
		Scopes:   splitSpaceSeparated(firstTagValue(xmlBody, "Scopes")),
	}
}

func firstTagValue(xmlBody string, localName string) string {
	if strings.TrimSpace(xmlBody) == "" || strings.TrimSpace(localName) == "" {
		return ""
	}
	pattern := `(?is)<(?:[a-zA-Z0-9_]+:)?` + regexp.QuoteMeta(localName) + `\b[^>]*>(.*?)</(?:[a-zA-Z0-9_]+:)?` + regexp.QuoteMeta(localName) + `>`
	matches := regexp.MustCompile(pattern).FindStringSubmatch(xmlBody)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func extractXMLAttributeFloat(xmlBody string, localName string, attrName string) (float64, bool) {
	if strings.TrimSpace(xmlBody) == "" || strings.TrimSpace(localName) == "" || strings.TrimSpace(attrName) == "" {
		return 0, false
	}
	pattern := `(?is)<(?:[a-zA-Z0-9_]+:)?` + regexp.QuoteMeta(localName) + `\b[^>]*\b` + regexp.QuoteMeta(attrName) + `\s*=\s*"(.*?)"[^>]*>`
	matches := regexp.MustCompile(pattern).FindStringSubmatch(xmlBody)
	if len(matches) < 2 {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(matches[1]), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func splitSpaceSeparated(raw string) []string {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return []string{}
	}
	uniq := map[string]struct{}{}
	result := make([]string, 0, len(parts))
	for _, item := range parts {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, exists := uniq[value]; exists {
			continue
		}
		uniq[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeStringSlice(base []string, next []string) []string {
	uniq := map[string]struct{}{}
	result := make([]string, 0, len(base)+len(next))
	for _, item := range base {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, exists := uniq[value]; exists {
			continue
		}
		uniq[value] = struct{}{}
		result = append(result, value)
	}
	for _, item := range next {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, exists := uniq[value]; exists {
			continue
		}
		uniq[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func ParseFloatOrDefault(raw any, fallback float64) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
