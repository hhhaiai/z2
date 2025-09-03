package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 配置变量（硬编码，不再从环境变量读取）
var (
	UPSTREAM_URL      = "https://chat.z.ai/api/chat/completions"
	MODELS_URL        = "https://chat.z.ai/api/models"
	DEFAULT_KEY       = "sk-your-key"
	ZAI_TOKEN         = ""
	MODEL_NAME        = "GLM-4.5"
	PORT              = ":7860"
	DEBUG_MODE        = true
	DEFAULT_STREAM    = true
	DASHBOARD_ENABLED = true
)

// 请求统计信息
type RequestStats struct {
	TotalRequests       int64
	SuccessfulRequests  int64
	FailedRequests      int64
	LastRequestTime     time.Time
	AverageResponseTime time.Duration
}

// 实时请求信息
type LiveRequest struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Duration  int64     `json:"duration"`
	UserAgent string    `json:"user_agent"`
}

// 全局变量
var (
	stats         RequestStats
	liveRequests  = []LiveRequest{}
	statsMutex    sync.Mutex
	requestsMutex sync.Mutex
	startTime     = time.Now() // 服务启动时间
)

// 思考内容处理策略
const (
	THINK_TAGS_MODE = "strip" // strip: 去除<details>标签；think: 转为<think>标签；raw: 保留原样
)

// 伪装前端头部（来自抓包）
const (
	X_FE_VERSION   = "prod-fe-1.0.76"
	BROWSER_UA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36"
	SEC_CH_UA      = "\"Not;A=Brand\";v=\"99\", \"Edge\";v=\"139\""
	SEC_CH_UA_MOB  = "?0"
	SEC_CH_UA_PLAT = "\"Windows\""
	ORIGIN_BASE    = "https://chat.z.ai"
)

// 匿名token开关
const ANON_TOKEN_ENABLED = true

// OpenAI 请求结构
type OpenAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// 上游请求结构
type UpstreamRequest struct {
	Stream          bool                   `json:"stream"`
	Model           string                 `json:"model"`
	Messages        []Message              `json:"messages"`
	Params          map[string]interface{} `json:"params"`
	Features        map[string]interface{} `json:"features"`
	BackgroundTasks map[string]bool        `json:"background_tasks,omitempty"`
	ChatID          string                 `json:"chat_id,omitempty"`
	ID              string                 `json:"id,omitempty"`
	MCPServers      []string               `json:"mcp_servers,omitempty"`
	ModelItem       struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		OwnedBy string `json:"owned_by"`
	} `json:"model_item,omitempty"`
	ToolServers []string          `json:"tool_servers,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
}

// OpenAI 响应结构
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message,omitempty"`
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// 上游SSE响应结构
type UpstreamData struct {
	Type  string         `json:"type"`
	Error *UpstreamError `json:"error,omitempty"`
	Data  struct {
		Phase        string         `json:"phase"`
		DeltaContent string         `json:"delta_content"`
		Done         bool           `json:"done"`
		Error        *UpstreamError `json:"error,omitempty"`
		Inner        *struct {
			Error *UpstreamError `json:"error,omitempty"`
		} `json:"inner,omitempty"`
	} `json:"data"`
}

type UpstreamError struct {
	Detail string `json:"detail"`
	Code   int    `json:"code"`
}

// 模型列表响应
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Name    string `json:"name,omitempty"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// 上游模型响应结构
type UpstreamModelsResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Info struct {
			IsActive  bool  `json:"is_active"`
			CreatedAt int64 `json:"created_at"`
		} `json:"info"`
	} `json:"data"`
}

// 记录请求统计信息
func recordRequestStats(startTime time.Time, path string, status int) {
	duration := time.Since(startTime)

	statsMutex.Lock()
	defer statsMutex.Unlock()

	stats.TotalRequests++
	stats.LastRequestTime = time.Now()

	if status >= 200 && status < 300 {
		stats.SuccessfulRequests++
	} else {
		stats.FailedRequests++
	}

	// 更新平均响应时间
	if stats.TotalRequests > 0 {
		totalDuration := stats.AverageResponseTime*time.Duration(stats.TotalRequests-1) + duration
		stats.AverageResponseTime = totalDuration / time.Duration(stats.TotalRequests)
	} else {
		stats.AverageResponseTime = duration
	}
}

// 添加实时请求信息
func addLiveRequest(method, path string, status int, duration time.Duration, _, userAgent string) {
	requestsMutex.Lock()
	defer requestsMutex.Unlock()

	request := LiveRequest{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Timestamp: time.Now(),
		Method:    method,
		Path:      path,
		Status:    status,
		Duration:  duration.Milliseconds(),
		UserAgent: userAgent,
	}

	liveRequests = append(liveRequests, request)

	// 只保留最近的100条请求
	if len(liveRequests) > 100 {
		liveRequests = liveRequests[1:]
	}
}

// 获取客户端IP地址
func getClientIP(r *http.Request) string {
	// 检查X-Forwarded-For头
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// 检查X-Real-IP头
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// 使用RemoteAddr
	ip := r.RemoteAddr
	// 移除端口号
	if strings.Contains(ip, ":") {
		ip = strings.Split(ip, ":")[0]
	}
	return ip
}

// 调试日志
func debugLog(format string, args ...interface{}) {
	if DEBUG_MODE {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// 获取匿名token（每次对话使用不同token，避免共享记忆）
func getAnonymousToken() (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	req, err := http.NewRequest("GET", ORIGIN_BASE+"/api/v1/auths/", nil)
	if err != nil {
		debugLog("创建匿名token请求失败: %v", err)
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	// 伪装浏览器头
	req.Header.Set("User-Agent", BROWSER_UA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("X-FE-Version", X_FE_VERSION)
	req.Header.Set("sec-ch-ua", SEC_CH_UA)
	req.Header.Set("sec-ch-ua-mobile", SEC_CH_UA_MOB)
	req.Header.Set("sec-ch-ua-platform", SEC_CH_UA_PLAT)
	req.Header.Set("Origin", ORIGIN_BASE)
	req.Header.Set("Referer", ORIGIN_BASE+"/")

	resp, err := client.Do(req)
	if err != nil {
		debugLog("匿名token请求失败: %v", err)
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			debugLog("关闭响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		debugLog("匿名token响应状态码异常: %d", resp.StatusCode)
		return "", fmt.Errorf("服务器响应错误，状态码: %d", resp.StatusCode)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		debugLog("匿名token响应解析失败: %v", err)
		return "", fmt.Errorf("响应解析失败: %w", err)
	}

	if body.Token == "" {
		debugLog("匿名token为空")
		return "", fmt.Errorf("获取到的token为空")
	}

	debugLog("匿名token获取成功: %s...", func() string {
		if len(body.Token) > 10 {
			return body.Token[:10]
		}
		return body.Token
	}())

	return body.Token, nil
}

// 获取模型列表
func getModels() []Model {
	// 获取token
	token := ZAI_TOKEN
	if ANON_TOKEN_ENABLED {
		if t, err := getAnonymousToken(); err == nil {
			token = t
		}
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	req, err := http.NewRequest("GET", MODELS_URL, nil)
	if err != nil {
		debugLog("创建模型请求失败: %v", err)
		return getDefaultModels()
	}

	// 设置请求头
	req.Header.Set("User-Agent", BROWSER_UA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("X-FE-Version", X_FE_VERSION)
	req.Header.Set("sec-ch-ua", SEC_CH_UA)
	req.Header.Set("sec-ch-ua-mobile", SEC_CH_UA_MOB)
	req.Header.Set("sec-ch-ua-platform", SEC_CH_UA_PLAT)
	req.Header.Set("Origin", ORIGIN_BASE)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		debugLog("获取模型列表失败: %v", err)
		return getDefaultModels()
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			debugLog("关闭模型响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		debugLog("模型列表响应状态异常: %d", resp.StatusCode)
		return getDefaultModels()
	}

	var upstreamResp UpstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstreamResp); err != nil {
		debugLog("解析模型列表失败: %v", err)
		return getDefaultModels()
	}

	var models []Model
	for _, m := range upstreamResp.Data {
		if !m.Info.IsActive {
			continue
		}

		modelName := m.Name
		if modelName == "" || !isEnglishLetter(modelName[0]) {
			modelName = formatModelName(m.ID)
		}

		models = append(models, Model{
			ID:      m.ID,
			Object:  "model",
			Name:    modelName,
			Created: m.Info.CreatedAt,
			OwnedBy: "z.ai",
		})
	}

	if len(models) == 0 {
		return getDefaultModels()
	}

	debugLog("获取到%d个模型", len(models))
	return models
}

// 格式化模型名
func formatModelName(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "-")
	if len(parts) == 1 {
		return strings.ToUpper(parts[0])
	}
	formatted := []string{strings.ToUpper(parts[0])}
	for _, p := range parts[1:] {
		if p == "" {
			formatted = append(formatted, "")
		} else if isDigit(p) {
			formatted = append(formatted, p)
		} else if hasEnglishLetter(p) {
			formatted = append(formatted, strings.Title(p))
		} else {
			formatted = append(formatted, p)
		}
	}
	return strings.Join(formatted, "-")
}

// 判断是否是英文字符
func isEnglishLetter(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

// 判断字符串是否全为数字
func isDigit(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

// 判断字符串是否包含英文字符
func hasEnglishLetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if isEnglishLetter(s[i]) {
			return true
		}
	}
	return false
}

// 获取默认模型列表
func getDefaultModels() []Model {
	return []Model{
		{
			ID:      "GLM-4.5",
			Object:  "model",
			Name:    "GLM-4.5",
			Created: time.Now().Unix(),
			OwnedBy: "z.ai",
		},
		{
			ID:      "0727-360B-API",
			Object:  "model",
			Name:    "GLM-4.5",
			Created: time.Now().Unix(),
			OwnedBy: "z.ai",
		},
	}
}

// 处理统计页面请求
func handleStats(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	statsMutex.Lock()
	currentStats := stats
	statsMutex.Unlock()

	requestsMutex.Lock()
	currentRequests := make([]LiveRequest, len(liveRequests))
	copy(currentRequests, liveRequests)
	requestsMutex.Unlock()

	// 计算运行时间
	uptime := time.Since(startTime)

	// 构建HTML页面
	html := fmt.Sprintf(`
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>OpenAI兼容服务统计信息</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; padding: 20px; background: #f5f5f5; }
        .container { max-width: 1200px; margin: 0 auto; }
        .header { background: white; padding: 20px; border-radius: 8px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 20px; margin-bottom: 20px; }
        .stat-card { background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .stat-value { font-size: 2em; font-weight: bold; color: #2563eb; }
        .stat-label { color: #6b7280; margin-top: 5px; }
        .requests-table { background: white; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .table-header { background: #f9fafb; padding: 15px; font-weight: bold; border-bottom: 1px solid #e5e7eb; }
        .table-row { padding: 10px 15px; border-bottom: 1px solid #f3f4f6; display: grid; grid-template-columns: 1fr 1fr 1fr 1fr 1fr; gap: 10px; }
        .status-200 { color: #059669; }
        .status-400, .status-401 { color: #dc2626; }
        .status-500, .status-502 { color: #b91c1c; }
        .refresh-btn { background: #2563eb; color: white; border: none; padding: 10px 20px; border-radius: 6px; cursor: pointer; }
        .refresh-btn:hover { background: #1d4ed8; }
    </style>
    <script>
        function refreshPage() { location.reload(); }
        setInterval(refreshPage, 30000); // 30秒自动刷新
    </script>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>OpenAI兼容API服务器 - 统计信息</h1>
            <p>服务运行时间: %s | 最后更新: %s</p>
            <button class="refresh-btn" onclick="refreshPage()">刷新数据</button>
        </div>
        
        <div class="stats-grid">
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">总请求数</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">成功请求</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">失败请求</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%.2fms</div>
                <div class="stat-label">平均响应时间</div>
            </div>
        </div>

        <div class="requests-table">
            <div class="table-header">最近请求记录 (最多显示50条)</div>
            <div class="table-row" style="font-weight: bold; background: #f9fafb;">
                <div>时间</div>
                <div>方法</div>
                <div>路径</div>
                <div>状态码</div>
                <div>响应时间</div>
            </div>`,
		uptime.Round(time.Second),
		time.Now().Format("2006-01-02 15:04:05"),
		currentStats.TotalRequests,
		currentStats.SuccessfulRequests,
		currentStats.FailedRequests,
		float64(currentStats.AverageResponseTime.Nanoseconds())/1000000,
	)

	// 添加最近的请求记录
	for i := len(currentRequests) - 1; i >= 0 && len(currentRequests)-i <= 50; i-- {
		req := currentRequests[i]
		statusClass := "status-200"
		if req.Status >= 400 && req.Status < 500 {
			statusClass = "status-400"
		} else if req.Status >= 500 {
			statusClass = "status-500"
		}

		html += fmt.Sprintf(`
            <div class="table-row">
                <div>%s</div>
                <div>%s</div>
                <div>%s</div>
                <div class="%s">%d</div>
                <div>%.2fms</div>
            </div>`,
			req.Timestamp.Format("15:04:05"),
			req.Method,
			req.Path,
			statusClass,
			req.Status,
			float64(req.Duration)/1000000, // Duration已经是纳秒，直接转换为毫秒
		)
	}

	html += `
        </div>
    </div>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
}

// 处理模型列表请求
func handleModels(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	response := ModelsResponse{
		Object: "list",
		Data:   getModels(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// 设置CORS头
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

// 处理聊天完成请求
func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	path := r.URL.Path
	clientIP := getClientIP(r)
	userAgent := r.UserAgent()

	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	debugLog("收到chat completions请求")

	//// 验证API Key（可选）
	// authHeader := r.Header.Get("Authorization")
	// if authHeader != "" {
	// 	if !strings.HasPrefix(authHeader, "Bearer ") {
	// 		debugLog("无效的Authorization头格式")
	// 		http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
	// 		// 记录请求统计
	// 		duration := time.Since(startTime)
	// 		recordRequestStats(startTime, path, http.StatusUnauthorized)
	// 		addLiveRequest(r.Method, path, http.StatusUnauthorized, duration, "", userAgent)
	// 		return
	// 	}

	// 	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	// 	if apiKey != DEFAULT_KEY {
	// 		debugLog("无效的API key: %s", apiKey)
	// 		http.Error(w, "Invalid API key", http.StatusUnauthorized)
	// 		// 记录请求统计
	// 		duration := time.Since(startTime)
	// 		recordRequestStats(startTime, path, http.StatusUnauthorized)
	// 		addLiveRequest(r.Method, path, http.StatusUnauthorized, duration, "", userAgent)
	// 		return
	// 	}
	// 	debugLog("API key验证通过")
	// } else {
	// 	debugLog("无Authorization头，允许匿名访问")
	// }

	// 读取请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		debugLog("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, duration, "", userAgent)
		return
	}

	// 解析请求
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		debugLog("JSON解析失败: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, duration, "", userAgent)
		return
	}

	// 如果客户端没有明确指定stream参数，使用默认值
	if !bytes.Contains(body, []byte(`"stream"`)) {
		req.Stream = DEFAULT_STREAM
		debugLog("客户端未指定stream参数，使用默认值: %v", DEFAULT_STREAM)
	}

	debugLog("请求解析成功 - 模型: %s, 流式: %v, 消息数: %d", req.Model, req.Stream, len(req.Messages))

	// 生成会话相关ID
	chatID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix())
	msgID := fmt.Sprintf("%d", time.Now().UnixNano())

	// 构造上游请求
	upstreamReq := UpstreamRequest{
		Stream:   true, // 总是使用流式从上游获取
		ChatID:   chatID,
		ID:       msgID,
		Model:    "0727-360B-API", // 上游实际模型ID
		Messages: req.Messages,
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{
			"enable_thinking": true,
		},
		BackgroundTasks: map[string]bool{
			"title_generation": false,
			"tags_generation":  false,
		},
		MCPServers: []string{},
		ModelItem: struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			OwnedBy string `json:"owned_by"`
		}{ID: "0727-360B-API", Name: "GLM-4.5", OwnedBy: "openai"},
		ToolServers: []string{},
		Variables: map[string]string{
			"{{USER_NAME}}":        "User",
			"{{USER_LOCATION}}":    "Unknown",
			"{{CURRENT_DATETIME}}": time.Now().Format("2006-01-02 15:04:05"),
		},
	}

	// 选择本次对话使用的token
	authToken := ZAI_TOKEN
	if ANON_TOKEN_ENABLED {
		if t, err := getAnonymousToken(); err == nil {
			authToken = t
			debugLog("匿名token获取成功: %s...", func() string {
				if len(t) > 10 {
					return t[:10]
				}
				return t
			}())
		} else {
			debugLog("匿名token获取失败，回退固定token: %v", err)
		}
	}

	// 调用上游API
	if req.Stream {
		handleStreamResponseWithIDs(w, upstreamReq, chatID, authToken, startTime, path, clientIP, userAgent)
	} else {
		handleNonStreamResponseWithIDs(w, upstreamReq, chatID, authToken, startTime, path, clientIP, userAgent)
	}
}

// 调用上游API并处理响应
func callUpstreamWithHeaders(upstreamReq UpstreamRequest, refererChatID string, authToken string) (*http.Response, error) {
	reqBody, err := json.Marshal(upstreamReq)
	if err != nil {
		debugLog("上游请求序列化失败: %v", err)
		return nil, err
	}

	debugLog("调用上游API: %s", UPSTREAM_URL)
	debugLog("上游请求体: %s", string(reqBody))

	req, err := http.NewRequest("POST", UPSTREAM_URL, bytes.NewBuffer(reqBody))
	if err != nil {
		debugLog("创建HTTP请求失败: %v", err)
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", BROWSER_UA)
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("sec-ch-ua", SEC_CH_UA)
	req.Header.Set("sec-ch-ua-mobile", SEC_CH_UA_MOB)
	req.Header.Set("sec-ch-ua-platform", SEC_CH_UA_PLAT)
	req.Header.Set("X-FE-Version", X_FE_VERSION)
	req.Header.Set("Origin", ORIGIN_BASE)
	req.Header.Set("Referer", ORIGIN_BASE+"/c/"+refererChatID)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		debugLog("上游请求失败: %v", err)
		return nil, err
	}

	debugLog("上游响应状态: %d %s", resp.StatusCode, resp.Status)
	return resp, nil
}

// 处理流式响应
func handleStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, startTime time.Time, path string, clientIP string, userAgent string) {
	debugLog("开始处理流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadGateway)
		addLiveRequest("POST", path, http.StatusBadGateway, duration, "", userAgent)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		debugLog("上游返回错误状态: %d", resp.StatusCode)
		// 读取错误响应体
		if DEBUG_MODE {
			body, _ := io.ReadAll(resp.Body)
			debugLog("上游错误响应: %s", string(body))
		}
		http.Error(w, "Upstream error", http.StatusBadGateway)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadGateway)
		addLiveRequest("POST", path, http.StatusBadGateway, duration, "", userAgent)
		return
	}

	// 用于策略2：总是展示thinking（配合标签处理）
	transformThinking := func(s string) string {
		// 去 <summary>…</summary>
		s = regexp.MustCompile(`(?s)<summary>.*?</summary>`).ReplaceAllString(s, "")
		// 清理残留自定义标签，如 </thinking>、<Full> 等
		s = strings.ReplaceAll(s, "</thinking>", "")
		s = strings.ReplaceAll(s, "<Full>", "")
		s = strings.ReplaceAll(s, "</Full>", "")
		s = strings.TrimSpace(s)
		switch THINK_TAGS_MODE {
		case "think":
			s = regexp.MustCompile(`<details[^>]*>`).ReplaceAllString(s, "<think>")
			s = strings.ReplaceAll(s, "</details>", "</think>")
		case "strip":
			s = regexp.MustCompile(`<details[^>]*>`).ReplaceAllString(s, "")
			s = strings.ReplaceAll(s, "</details>", "")
		}
		// 处理每行前缀 "> "（包括起始位置）
		s = strings.TrimPrefix(s, "> ")
		s = strings.ReplaceAll(s, "\n> ", "\n")
		return strings.TrimSpace(s)
	}

	// 设置SSE头部
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 发送第一个chunk（role）
	firstChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   MODEL_NAME,
		Choices: []Choice{
			{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			},
		},
	}
	writeSSEChunk(w, firstChunk)
	flusher.Flush()

	// 读取上游SSE流
	debugLog("开始读取上游SSE流")
	scanner := bufio.NewScanner(resp.Body)
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		debugLog("收到SSE数据 (第%d行): %s", lineCount, dataStr)

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			debugLog("SSE数据解析失败: %v", err)
			continue
		}

		// 错误检测（data.error 或 data.data.error 或 顶层error）
		if (upstreamData.Error != nil) || (upstreamData.Data.Error != nil) || (upstreamData.Data.Inner != nil && upstreamData.Data.Inner.Error != nil) {
			errObj := upstreamData.Error
			if errObj == nil {
				errObj = upstreamData.Data.Error
			}
			if errObj == nil && upstreamData.Data.Inner != nil {
				errObj = upstreamData.Data.Inner.Error
			}
			debugLog("上游错误: code=%d, detail=%s", errObj.Code, errObj.Detail)
			// 结束下游流
			endChunk := OpenAIResponse{
				ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   MODEL_NAME,
				Choices: []Choice{{Index: 0, Delta: Delta{}, FinishReason: "stop"}},
			}
			writeSSEChunk(w, endChunk)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}

		debugLog("解析成功 - 类型: %s, 阶段: %s, 内容长度: %d, 完成: %v",
			upstreamData.Type, upstreamData.Data.Phase, len(upstreamData.Data.DeltaContent), upstreamData.Data.Done)

		// 策略2：总是展示thinking + answer
		if upstreamData.Data.DeltaContent != "" {
			var out = upstreamData.Data.DeltaContent
			if upstreamData.Data.Phase == "thinking" {
				out = transformThinking(out)
			}
			if out != "" {
				debugLog("发送内容(%s): %s", upstreamData.Data.Phase, out)
				chunk := OpenAIResponse{
					ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   MODEL_NAME,
					Choices: []Choice{
						{
							Index: 0,
							Delta: Delta{Content: out},
						},
					},
				}
				writeSSEChunk(w, chunk)
				flusher.Flush()
			}
		}

		// 检查是否结束
		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到流结束信号")
			// 发送结束chunk
			endChunk := OpenAIResponse{
				ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   MODEL_NAME,
				Choices: []Choice{
					{
						Index:        0,
						Delta:        Delta{},
						FinishReason: "stop",
					},
				},
			}
			writeSSEChunk(w, endChunk)
			flusher.Flush()

			// 发送[DONE]
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			debugLog("流式响应完成，共处理%d行", lineCount)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		debugLog("扫描器错误: %v", err)
	}

	// 记录成功请求统计
	duration := time.Since(startTime)
	recordRequestStats(startTime, path, http.StatusOK)
	addLiveRequest("POST", path, http.StatusOK, duration, "", userAgent)
}

// 写入SSE块
func writeSSEChunk(w http.ResponseWriter, chunk OpenAIResponse) {
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// 处理非流式响应
func handleNonStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, startTime time.Time, path string, clientIP string, userAgent string) {
	debugLog("开始处理非流式响应 (chat_id=%s)", chatID)

	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		http.Error(w, "Failed to call upstream", http.StatusBadGateway)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadGateway)
		addLiveRequest("POST", path, http.StatusBadGateway, duration, "", userAgent)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		debugLog("上游返回错误状态: %d", resp.StatusCode)
		// 读取错误响应体
		if DEBUG_MODE {
			body, _ := io.ReadAll(resp.Body)
			debugLog("上游错误响应: %s", string(body))
		}
		http.Error(w, "Upstream error", http.StatusBadGateway)
		// 记录请求统计
		duration := time.Since(startTime)
		recordRequestStats(startTime, path, http.StatusBadGateway)
		addLiveRequest("POST", path, http.StatusBadGateway, duration, "", userAgent)
		return
	}

	// 收集完整响应（策略2：thinking与answer都纳入，thinking转换）
	var fullContent strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	debugLog("开始收集完整响应内容")

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "" {
			continue
		}

		var upstreamData UpstreamData
		if err := json.Unmarshal([]byte(dataStr), &upstreamData); err != nil {
			continue
		}

		if upstreamData.Data.DeltaContent != "" {
			out := upstreamData.Data.DeltaContent
			if upstreamData.Data.Phase == "thinking" {
				out = func(s string) string {
					// 同步一份转换逻辑（与流式一致）
					s = regexp.MustCompile(`(?s)<summary>.*?</summary>`).ReplaceAllString(s, "")
					s = strings.ReplaceAll(s, "</thinking>", "")
					s = strings.ReplaceAll(s, "<Full>", "")
					s = strings.ReplaceAll(s, "</Full>", "")
					s = strings.TrimSpace(s)
					switch THINK_TAGS_MODE {
					case "think":
						s = regexp.MustCompile(`<details[^>]*>`).ReplaceAllString(s, "<think>")
						s = strings.ReplaceAll(s, "</details>", "</think>")
					case "strip":
						s = regexp.MustCompile(`<details[^>]*>`).ReplaceAllString(s, "")
						s = strings.ReplaceAll(s, "</details>", "")
					}
					s = strings.TrimPrefix(s, "> ")
					s = strings.ReplaceAll(s, "\n> ", "\n")
					return strings.TrimSpace(s)
				}(out)
			}
			if out != "" {
				fullContent.WriteString(out)
			}
		}

		if upstreamData.Data.Done || upstreamData.Data.Phase == "done" {
			debugLog("检测到完成信号，停止收集")
			break
		}
	}

	finalContent := fullContent.String()
	debugLog("内容收集完成，最终长度: %d", len(finalContent))

	// 构造完整响应
	response := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   MODEL_NAME,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: finalContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	debugLog("非流式响应发送完成")

	// 记录成功请求统计
	duration := time.Since(startTime)
	recordRequestStats(startTime, path, http.StatusOK)
	addLiveRequest("POST", path, http.StatusOK, duration, "", userAgent)
}

// 处理OPTIONS请求
func handleOptions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func main() {
	// 注册路由
	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/api/v1/models", handleModels)
	http.HandleFunc("/api/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/stats", handleStats)
	http.HandleFunc("/", handleStats) // 首页显示统计信息

	log.Printf("OpenAI兼容API服务器启动在端口%s", PORT)
	log.Printf("模型: %s", MODEL_NAME)
	log.Printf("上游: %s", UPSTREAM_URL)
	log.Printf("Debug模式: %v", DEBUG_MODE)
	log.Printf("默认流式响应: %v", DEFAULT_STREAM)
	log.Printf("匿名模式: %v", ANON_TOKEN_ENABLED)
	log.Printf("---------------------------------------------------------------------")
	log.Printf("🌐 服务器地址: http://localhost%s", PORT)
	log.Printf("📊 统计页面: http://localhost%s/stats", PORT)
	log.Printf("📋 模型列表: http://localhost%s/v1/models", PORT)
	log.Printf("💬 聊天接口: http://localhost%s/v1/chat/completions", PORT)
	log.Printf("---------------------------------------------------------------------")
	log.Fatal(http.ListenAndServe(PORT, nil))
}
