package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var providerHTTPClient = &http.Client{Timeout: 15 * time.Second}

func (s *Service) sendProviderMessage(ctx context.Context, provider string, params map[string]any, title string, content string) (map[string]any, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	if title == "" {
		title = "[Gover] Bot notification"
	}
	if content == "" {
		content = title
	}

	switch provider {
	case "dingtalk", "ding":
		return s.sendDingTalkMessage(ctx, params, title, content)
	case "telegram", "tg":
		return s.sendTelegramMessage(ctx, params, title, content)
	case "pushoo", "pushoo-chan", "pushoo_chan":
		return s.sendPushooMessage(ctx, params, title, content)
	default:
		return nil, errors.New("unsupported provider: " + provider)
	}
}

func (s *Service) resolveProviderAPIKey(ctx context.Context, keyName string) string {
	item, err := s.store.GetAPIKeyByName(ctx, keyName)
	if err != nil || item == nil {
		return ""
	}
	return strings.TrimSpace(item.APIKey)
}

func (s *Service) sendDingTalkMessage(ctx context.Context, params map[string]any, title string, content string) (map[string]any, error) {
	endpoint := strings.TrimSpace(asString(params["webhook"]))
	if endpoint == "" {
		endpoint = strings.TrimSpace(asString(params["url"]))
	}
	if endpoint == "" {
		apiKey := s.resolveProviderAPIKey(ctx, "dingtalk")
		if strings.HasPrefix(strings.ToLower(apiKey), "http://") || strings.HasPrefix(strings.ToLower(apiKey), "https://") {
			endpoint = apiKey
		} else if apiKey != "" {
			endpoint = "https://oapi.dingtalk.com/robot/send?access_token=" + apiKey
		}
	}
	if endpoint == "" {
		return nil, errors.New("dingtalk endpoint is empty")
	}

	secret := strings.TrimSpace(asString(params["secret"]))
	if secret == "" {
		secret = strings.TrimSpace(asString(params["signSecret"]))
	}
	if secret != "" {
		signedURL, err := signDingTalkURL(endpoint, secret)
		if err != nil {
			return nil, err
		}
		endpoint = signedURL
	}

	payload := map[string]any{
		"msgtype": "text",
		"text": map[string]any{
			"content": strings.TrimSpace(title + "\n" + content),
		},
	}
	resp, status, bodyText, err := postJSON(ctx, endpoint, payload, map[string]string{
		"User-Agent": "gover-provider/1.0",
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errors.New("dingtalk http status=" + strconvText(status) + " body=" + truncateText(bodyText, 300))
	}
	if code, ok := readInt(resp, "errcode"); ok && code != 0 {
		return nil, errors.New("dingtalk errcode=" + strconvText(code) + " errmsg=" + strings.TrimSpace(asString(resp["errmsg"])))
	}
	return map[string]any{
		"provider": "dingtalk",
		"status":   status,
		"endpoint": endpoint,
		"result":   resp,
	}, nil
}

func (s *Service) sendTelegramMessage(ctx context.Context, params map[string]any, title string, content string) (map[string]any, error) {
	token := strings.TrimSpace(asString(params["botToken"]))
	if token == "" {
		token = strings.TrimSpace(asString(params["token"]))
	}
	chatID := strings.TrimSpace(asString(params["chatId"]))
	if chatID == "" {
		chatID = strings.TrimSpace(asString(params["chatID"]))
	}

	if token == "" {
		apiKey := s.resolveProviderAPIKey(ctx, "telegram")
		if apiKey != "" {
			parts := strings.SplitN(apiKey, "|", 2)
			if len(parts) == 2 {
				token = strings.TrimSpace(parts[0])
				if chatID == "" {
					chatID = strings.TrimSpace(parts[1])
				}
			} else {
				token = strings.TrimSpace(apiKey)
			}
		}
	}
	if token == "" {
		return nil, errors.New("telegram botToken is empty")
	}
	if chatID == "" {
		return nil, errors.New("telegram chatId is empty")
	}

	message := strings.TrimSpace(title + "\n" + content)
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     message,
		"disable_web_page_preview": true,
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	resp, status, bodyText, err := postJSON(ctx, endpoint, payload, map[string]string{
		"User-Agent": "gover-provider/1.0",
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errors.New("telegram http status=" + strconvText(status) + " body=" + truncateText(bodyText, 300))
	}
	if ok, exists := resp["ok"].(bool); exists && !ok {
		return nil, errors.New("telegram api failed: " + truncateText(bodyText, 300))
	}
	return map[string]any{
		"provider": "telegram",
		"status":   status,
		"endpoint": endpoint,
		"chatId":   chatID,
		"result":   resp,
	}, nil
}

func (s *Service) sendPushooMessage(ctx context.Context, params map[string]any, title string, content string) (map[string]any, error) {
	endpoint := strings.TrimSpace(asString(params["endpoint"]))
	if endpoint == "" {
		endpoint = strings.TrimSpace(asString(params["url"]))
	}
	if endpoint == "" {
		endpoint = s.resolveProviderAPIKey(ctx, "pushoo")
	}
	if endpoint == "" {
		return nil, errors.New("pushoo endpoint is empty")
	}
	endpoint = normalizePushooEndpoint(endpoint)
	token := strings.TrimSpace(asString(params["token"]))
	if token == "" {
		token = s.resolveProviderAPIKey(ctx, "pushoo_token")
	}
	chanName := strings.TrimSpace(asString(params["chan"]))
	if chanName == "" {
		chanName = strings.TrimSpace(asString(params["channel"]))
	}

	payload := map[string]any{
		"text": title,
		"desp": content,
	}
	if token != "" {
		payload["token"] = token
	}
	if chanName != "" {
		payload["chan"] = chanName
	}

	resp, status, bodyText, err := postJSON(ctx, endpoint, payload, map[string]string{
		"User-Agent": "gover-provider/1.0",
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, errors.New("pushoo http status=" + strconvText(status) + " body=" + truncateText(bodyText, 300))
	}
	if code, ok := readInt(resp, "code"); ok && code != 0 {
		return nil, errors.New("pushoo api code=" + strconvText(code) + " body=" + truncateText(bodyText, 300))
	}
	return map[string]any{
		"provider": "pushoo",
		"status":   status,
		"endpoint": endpoint,
		"result":   resp,
	}, nil
}

func postJSON(ctx context.Context, endpoint string, payload any, headers map[string]string) (map[string]any, int, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	bodyText := strings.TrimSpace(string(bodyBytes))
	result := map[string]any{}
	if strings.TrimSpace(bodyText) != "" {
		_ = json.Unmarshal(bodyBytes, &result)
	}
	return result, resp.StatusCode, bodyText, nil
}

func signDingTalkURL(endpoint string, secret string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	query := parsed.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", signature)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func normalizePushooEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if !strings.HasPrefix(strings.ToLower(value), "http://") && !strings.HasPrefix(strings.ToLower(value), "https://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	if !strings.HasSuffix(parsed.Path, "/send") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/send"
	}
	return parsed.String()
}

func readInt(payload map[string]any, key string) (int, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		i, _ := v.Int64()
		return int(i), true
	default:
		return 0, false
	}
}

func strconvText(value int) string {
	return fmt.Sprintf("%d", value)
}
