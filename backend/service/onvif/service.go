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
	Token string `json:"token"`
}

type DiscoveredDevice struct {
	Endpoint string   `json:"endpoint"`
	XAddrs   []string `json:"xAddrs"`
	URN      string   `json:"urn"`
	Types    []string `json:"types"`
	Scopes   []string `json:"scopes"`
	From     string   `json:"from"`
}

func New() *Service {
	return &Service{
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (s *Service) Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if timeout > 15*time.Second {
		timeout = 15 * time.Second
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	messageID, err := randomUUID()
	if err != nil {
		return nil, err
	}
	probe := buildDiscoveryProbe("urn:uuid:" + messageID)
	target := &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 3702}
	if _, err := conn.WriteTo([]byte(probe), target); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(time.Now().Add(350 * time.Millisecond))
	seen := map[string]DiscoveredDevice{}
	buffer := make([]byte, 64<<10)

	for {
		if time.Now().After(deadline) {
			break
		}
		n, remoteAddr, readErr := conn.ReadFrom(buffer)
		if readErr != nil {
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				_ = conn.SetReadDeadline(time.Now().Add(350 * time.Millisecond))
				continue
			}
			break
		}
		xmlBody := string(buffer[:n])
		device := parseDiscoveryResponse(xmlBody)
		if len(device.XAddrs) == 0 {
			continue
		}
		device.From = remoteAddr.String()
		if strings.TrimSpace(device.Endpoint) == "" {
			device.Endpoint = device.XAddrs[0]
		}
		key := strings.TrimSpace(device.URN)
		if key == "" {
			key = strings.TrimSpace(device.Endpoint)
		}
		if key == "" {
			continue
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
			continue
		}
		seen[key] = device
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
	matches := regexp.MustCompile(`token="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, errors.New("no onvif profiles found")
	}
	uniq := map[string]struct{}{}
	profiles := make([]Profile, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		token := strings.TrimSpace(m[1])
		if token == "" {
			continue
		}
		if _, ok := uniq[token]; ok {
			continue
		}
		uniq[token] = struct{}{}
		profiles = append(profiles, Profile{Token: token})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Token < profiles[j].Token })
	return profiles, nil
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
	default:
		return nil, fmt.Errorf("unsupported ptz action: %s", action)
	}

	return map[string]any{
		"ok":           true,
		"action":       action,
		"profileToken": req.ProfileToken,
		"ptzXAddr":     caps.PTZXAddr,
		"mediaXAddr":   caps.MediaXAddr,
	}, nil
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

func buildDiscoveryProbe(messageID string) string {
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
		`<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body>` +
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
