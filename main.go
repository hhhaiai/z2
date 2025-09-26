package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 配置变量（从环境变量读取）
var (
	UPSTREAM_URL              string
	DEFAULT_KEY               string
	ZAI_TOKEN                 string
	MODEL_NAME                string // 未使用，因为现在动态获取
	PORT                      string
	DEBUG_MODE                bool
	DEFAULT_STREAM            bool
	ENABLE_THINKING           bool
	MODELS_URL                string // 新增：模型列表URL
	DEFAULT_UPSTREAM_MODEL_ID string // 新增：默认上游模型ID
)

// 请求统计信息
type RequestStats struct {
	TotalRequests       int64         `json:"TotalRequests"`
	SuccessfulRequests  int64         `json:"SuccessfulRequests"`
	FailedRequests      int64         `json:"FailedRequests"`
	LastRequestTime     time.Time     `json:"LastRequestTime"`
	AverageResponseTime time.Duration `json:"AverageResponseTime"`
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

// 上游模型响应结构 (新增)
type UpstreamModelsResponse struct {
	Object string          `json:"object"`
	Data   []UpstreamModel `json:"data"`
}

type UpstreamModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Info    struct {
		IsActive  bool  `json:"is_active"`
		CreatedAt int64 `json:"created_at"`
	} `json:"info"`
}

// OpenAI 请求结构 (chat)
type OpenAIRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages,omitempty"`
	Prompt         any       `json:"prompt,omitempty"` // 支持 /v1/completions 的 prompt（可能是 string 或 []string）
	Stream         bool      `json:"stream,omitempty"`
	Temperature    float64   `json:"temperature,omitempty"`
	MaxTokens      int       `json:"max_tokens,omitempty"`
	EnableThinking *bool     `json:"enable_thinking,omitempty"`
	// 兼容字段（completions）
	N                     int  `json:"n,omitempty"`
	TopP                  any  `json:"top_p,omitempty"`
	Stop                  any  `json:"stop,omitempty"`
	MaxTokensCompletions  int  `json:"max_tokens,omitempty"`
	BestOf                int  `json:"best_of,omitempty"`
	PresencePenalty       any  `json:"presence_penalty,omitempty"`
	FrequencyPenalty      any  `json:"frequency_penalty,omitempty"`
	LogitBias             any  `json:"logit_bias,omitempty"`
	StrictStreamDelimiter bool `json:"-"` // internal
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
	Type string `json:"type"`
	Data struct {
		DeltaContent string         `json:"delta_content"`
		Phase        string         `json:"phase"`
		Done         bool           `json:"done"`
		Usage        Usage          `json:"usage,omitempty"`
		Error        *UpstreamError `json:"error,omitempty"`
		Inner        *struct {
			Error *UpstreamError `json:"error,omitempty"`
		} `json:"data,omitempty"`
	} `json:"data"`
	Error *UpstreamError `json:"error,omitempty"`
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
	ID      string `json:"id"` // 保持ID字段
	Object  string `json:"object"`
	Name    string `json:"name"` // 新增Name字段，用于显示
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// 全局变量
var (
	stats         RequestStats
	liveRequests  = []LiveRequest{} // 初始化为空数组，而不是 nil
	statsMutex    sync.Mutex
	requestsMutex sync.Mutex
	modelsCache   []Model      // 新增：缓存模型列表
	modelsMutex   sync.RWMutex // 新增：保护模型缓存的读写锁
)

// 思考内容处理策略
const (
	THINK_TAGS_MODE = "strip" // strip: 去除<details>标签；think: 转为<think>标签；raw: 保留原样
)

// 伪装前端头部（来自抓包）
const (
	X_FE_VERSION   = "prod-fe-1.0.70"
	BROWSER_UA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 Edg/139.0.0.0"
	SEC_CH_UA      = "\"Not;A=Brand\";v=\"99\", \"Microsoft Edge\";v=\"139\", \"Chromium\";v=\"139\""
	SEC_CH_UA_MOB  = "?0"
	SEC_CH_UA_PLAT = "\"Windows\""
	ORIGIN_BASE    = "https://chat.z.ai"
)

// 匿名token开关
const ANON_TOKEN_ENABLED = true

// 从环境变量初始化配置
func initConfig() {
	UPSTREAM_URL = getEnv("UPSTREAM_URL", "https://chat.z.ai/api/chat/completions")
	DEFAULT_KEY = getEnv("DEFAULT_KEY", "sk-your-key")
	ZAI_TOKEN = getEnv("ZAI_TOKEN", "")
	MODEL_NAME = getEnv("MODEL_NAME", "GLM-4.5") // 未使用，但保留
	PORT = getEnv("PORT", "7860")
	MODELS_URL = getEnv("MODELS_URL", "https://chat.z.ai/api/models")                // 新增
	DEFAULT_UPSTREAM_MODEL_ID = getEnv("DEFAULT_UPSTREAM_MODEL_ID", "0727-360B-API") // 新增
	// 处理PORT格式，确保有冒号前缀
	if !strings.HasPrefix(PORT, ":") {
		PORT = ":" + PORT
	}
	DEBUG_MODE = getEnv("DEBUG_MODE", "true") == "true"
	DEFAULT_STREAM = getEnv("DEFAULT_STREAM", "true") == "true"
	ENABLE_THINKING = getEnv("ENABLE_THINKING", "true") == "true"
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

// 获取实时请求数据（用于SSE）
func getLiveRequestsData() []byte {
	requestsMutex.Lock()
	defer requestsMutex.Unlock()
	// 确保 liveRequests 不为 nil
	if liveRequests == nil {
		liveRequests = []LiveRequest{}
	}
	data, err := json.Marshal(liveRequests)
	if err != nil {
		// 如果序列化失败，返回空数组
		emptyArray := []LiveRequest{}
		data, _ = json.Marshal(emptyArray)
	}
	return data
}

// 获取统计数据（用于SSE）
func getStatsData() []byte {
	statsMutex.Lock()
	defer statsMutex.Unlock()
	data, _ := json.Marshal(stats)
	return data
}

// 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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

// 检查字符是否为英文字母
func isEnglishLetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

// 检查字符串是否包含英文字母
func hasEnglishLetter(s string) bool {
	for _, r := range s {
		if isEnglishLetter(r) {
			return true
		}
	}
	return false
}

// 检查字符串是否为纯数字
func isDigit(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
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
			// Use Title for better capitalization of letters
			formatted = append(formatted, strings.Title(p))
		} else {
			formatted = append(formatted, p)
		}
	}
	return strings.Join(formatted, "-")
}

// 获取模型列表 (新增)
func getModels() []Model {
	modelsMutex.RLock()
	cachedModels := modelsCache
	modelsMutex.RUnlock()

	if cachedModels != nil {
		return cachedModels
	}

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
		if modelName == "" || !isEnglishLetter([]rune(modelName)[0]) { // 确保Name不为空且首字符是字母
			modelName = formatModelName(m.ID)
		}

		models = append(models, Model{
			ID:      m.ID,
			Object:  "model",
			Name:    modelName,        // 使用格式化后的名称或原始Name
			Created: m.Info.CreatedAt, // 使用上游的CreatedAt
			OwnedBy: "z.ai",
		})
	}

	if len(models) == 0 {
		return getDefaultModels()
	}

	// 更新缓存
	modelsMutex.Lock()
	modelsCache = models
	modelsMutex.Unlock()

	debugLog("获取到%d个模型", len(models))
	return models
}

// 获取默认模型列表（获取失败时使用）
func getDefaultModels() []Model {
	return []Model{
		{
			ID:      "0727-360B-API", // 与DEFAULT_UPSTREAM_MODEL_ID一致
			Object:  "model",
			Name:    "GLM-4.5", // 或根据ID格式化
			Created: time.Now().Unix(),
			OwnedBy: "z.ai",
		},
	}
}

// debug日志函数
func debugLog(format string, args ...interface{}) {
	if DEBUG_MODE {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// 获取匿名token（每次对话使用不同token，避免共享记忆）
func getAnonymousToken() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", ORIGIN_BASE+"/api/v1/auths/", nil)
	if err != nil {
		return "", err
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
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anon token status=%d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("anon token empty")
	}
	return body.Token, nil
}

func main() {
	// 初始化配置
	initConfig()
	// 全局 CORS OPTIONS 路由（所有路由都会被预检）
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 如果请求精确匹配某些路径会在各自的 handler 中被覆盖
		handleDashboard(w, r)
	})

	// 注册路由（OpenAI 兼容）
	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/v1/completions", handleCompletions) // 兼容 completions -> 转 chat
	// http.HandleFunc("/docs", handleAPIDocs)
	// 备选路径
	http.HandleFunc("/api/v1/models", handleModels)
	http.HandleFunc("/api/v1/chat/completions", handleChatCompletions)
	http.HandleFunc("/hf/v1/models", handleModels)
	http.HandleFunc("/hf/v1/chat/completions", handleChatCompletions)

	log.Printf("OpenAI兼容API服务器启动在端口%s", PORT)
	log.Printf("本地访问: http://localhost%s", PORT)
	log.Printf("模型: %s", MODEL_NAME)
	log.Printf("上游: %s", UPSTREAM_URL)
	log.Printf("Debug模式: %v", DEBUG_MODE)
	log.Printf("默认流式响应: %v", DEFAULT_STREAM)
	log.Printf("思考功能: %v", ENABLE_THINKING)
	log.Fatal(http.ListenAndServe(PORT, nil))
}

// 全局处理 OPTIONS 并设置 CORS（供所有 handler 调用）
func handleOptionsGlobal(w http.ResponseWriter, r *http.Request) bool {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	// 只允许GET请求
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetURL := "https://z.ai/" // 替换为你想要重定向到的实际URL
	http.Redirect(w, r, targetURL, http.StatusSeeOther)
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

// 修改 handleModels 函数，调用 getModels
func handleModels(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if handleOptionsGlobal(w, r) {
		return
	}

	response := ModelsResponse{
		Object: "list",
		Data:   getModels(), // 调用 getModels 获取列表
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// /v1/completions 兼容：把 prompt -> messages (user) 然后复用 chat handler 逻辑（非流式/流式都支持）
func handleCompletions(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if handleOptionsGlobal(w, r) {
		return
	}
	startTime := time.Now()
	path := r.URL.Path
	clientIP := getClientIP(r)
	userAgent := r.UserAgent()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpErrorJSON(w, "Failed to read body", http.StatusBadRequest)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, time.Since(startTime), "", userAgent)
		return
	}
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpErrorJSON(w, "Invalid JSON", http.StatusBadRequest)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, time.Since(startTime), "", userAgent)
		return
	}
	// try to map prompt -> messages if messages empty
	if len(req.Messages) == 0 && req.Prompt != nil {
		switch p := req.Prompt.(type) {
		case string:
			req.Messages = []Message{{Role: "user", Content: p}}
		case []interface{}:
			// join array into one message
			var sb strings.Builder
			for _, it := range p {
				sb.WriteString(fmt.Sprintf("%v\n", it))
			}
			req.Messages = []Message{{Role: "user", Content: sb.String()}}
		default:
			req.Messages = []Message{{Role: "user", Content: fmt.Sprintf("%v", p)}}
		}
	}
	r2 := *r
	r2.Body = io.NopCloser(bytes.NewReader(body))
	handleChatCompletions(w, &r2)
	// 记录统计在 handleChatCompletions 完成
	_ = startTime
	_ = clientIP
}

// 将错误以 OpenAI-style JSON 返回
func httpErrorJSON(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "invalid_request_error",
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// 验证 Authorization：兼容 OpenAI Bearer header（此处不强制固定 key，保留匿名 token 逻辑）
func validateAuth(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("Missing Authorization header")
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("Invalid Authorization header")
	}
	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	if apiKey == "" {
		return "", fmt.Errorf("Empty Bearer token")
	}
	return apiKey, nil
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	path := r.URL.Path
	clientIP := getClientIP(r)
	userAgent := r.UserAgent()
	setCORSHeaders(w)
	if handleOptionsGlobal(w, r) {
		return
	}
	debugLog("收到chat completions请求")

	// 验证API Key（只要存在 Bearer 即视为通过，以兼容 OpenAI 客户端）
	_, authErr := validateAuth(r)
	if authErr != nil {
		debugLog("缺少或无效的Authorization头: %v", authErr)
		httpErrorJSON(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
		recordRequestStats(startTime, path, http.StatusUnauthorized)
		addLiveRequest(r.Method, path, http.StatusUnauthorized, time.Since(startTime), "", userAgent)
		return
	}

	// 读取请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		debugLog("读取请求体失败: %v", err)
		httpErrorJSON(w, "Failed to read request body", http.StatusBadRequest)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, time.Since(startTime), "", userAgent)
		return
	}
	// 解析请求
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		debugLog("JSON解析失败: %v", err)
		httpErrorJSON(w, "Invalid JSON", http.StatusBadRequest)
		recordRequestStats(startTime, path, http.StatusBadRequest)
		addLiveRequest(r.Method, path, http.StatusBadRequest, time.Since(startTime), "", userAgent)
		return
	}

	// --- 模型映射逻辑 ---
	models := getModels()
	modelExists := false
	for _, m := range models {
		if m.ID == req.Model {
			modelExists = true
			break
		}
	}
	actualUpstreamModelID := req.Model // 默认使用请求的模型ID
	if !modelExists {
		debugLog("未知模型 '%s'，映射到默认上游模型 '%s'", req.Model, DEFAULT_UPSTREAM_MODEL_ID)
		actualUpstreamModelID = DEFAULT_UPSTREAM_MODEL_ID // 映射到默认模型
	}
	// --- 模型映射逻辑结束 ---

	// 如果客户端没有明确指定stream参数，使用默认值
	if !bytes.Contains(body, []byte(`"stream"`)) {
		req.Stream = DEFAULT_STREAM
		debugLog("客户端未指定stream参数，使用默认值: %v", DEFAULT_STREAM)
	}
	debugLog("请求解析成功 - 模型: %s (映射后: %s), 流式: %v, 消息数: %d", req.Model, actualUpstreamModelID, req.Stream, len(req.Messages))

	// 生成会话相关ID
	chatID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix())
	msgID := fmt.Sprintf("%d", time.Now().UnixNano())

	// 决定是否启用思考功能：优先使用请求参数，其次使用环境变量
	enableThinking := ENABLE_THINKING // 默认使用环境变量值
	if req.EnableThinking != nil {
		enableThinking = *req.EnableThinking
		debugLog("使用请求参数中的思考功能设置: %v", enableThinking)
	} else {
		debugLog("使用环境变量中的思考功能设置: %v", enableThinking)
	}

	// 构造上游请求 - 使用映射后的模型ID
	upstreamReq := UpstreamRequest{
		Stream:   true, // 总是使用流式从上游获取
		ChatID:   chatID,
		ID:       msgID,
		Model:    actualUpstreamModelID, // 使用映射后的ID
		Messages: req.Messages,
		Params:   map[string]interface{}{},
		Features: map[string]interface{}{
			"enable_thinking": enableThinking,
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
		}{ID: actualUpstreamModelID, Name: req.Model, OwnedBy: "openai"}, // ModelItem.ID也用映射后的，Name可以保留原始请求的ID或按需设置
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

	// 调用上游API，传入原始请求的模型ID用于响应
	if req.Stream {
		handleStreamResponseWithIDs(w, upstreamReq, chatID, authToken, startTime, path, clientIP, userAgent, req.Model) // 传入原始模型ID
	} else {
		handleNonStreamResponseWithIDs(w, upstreamReq, chatID, authToken, startTime, path, clientIP, userAgent, req.Model) // 传入原始模型ID
	}
}

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

// 将 SSE chunk 写为 OpenAI 风格：每个 data: <json>\n\n
func writeSSEChunk(w http.ResponseWriter, chunk OpenAIResponse) {
	data, _ := json.Marshal(chunk)
	// 必须以双换行结尾以符合 SSE 事件分隔
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// 修改 handleStreamResponseWithIDs 函数，增加原始模型ID参数
func handleStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, startTime time.Time, path string, clientIP, userAgent string, originalModelID string) { // 增加 originalModelID 参数
	debugLog("开始处理流式响应 (chat_id=%s, original_model=%s)", chatID, originalModelID)
	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		httpErrorJSON(w, "Failed to call upstream", http.StatusBadGateway)
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
		httpErrorJSON(w, "Upstream error", http.StatusBadGateway)
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
		s = strings.ReplaceAll(s, "\n> ", "\n") // <--- 修正换行符
		return strings.TrimSpace(s)
	}

	// 设置SSE头部（严格遵循 SSE）
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErrorJSON(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 发送第一个chunk（role），使用原始模型ID
	firstChunk := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   originalModelID, // 使用原始模型ID
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
			// 结束下游流：发送 finish chunk 并 [DONE]
			endChunk := OpenAIResponse{
				ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   originalModelID,
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
					Model:   originalModelID, // 使用原始模型ID
					Choices: []Choice{
						{
							Index: 0,
							Delta: Delta{Content: out},
						},
					},
				}
				// 写入并 flush
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
				Model:   originalModelID, // 使用原始模型ID
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

// 修改 handleNonStreamResponseWithIDs 函数，增加原始模型ID参数
func handleNonStreamResponseWithIDs(w http.ResponseWriter, upstreamReq UpstreamRequest, chatID string, authToken string, startTime time.Time, path string, clientIP, userAgent string, originalModelID string) { // 增加 originalModelID 参数
	debugLog("开始处理非流式响应 (chat_id=%s, original_model=%s)", chatID, originalModelID)
	resp, err := callUpstreamWithHeaders(upstreamReq, chatID, authToken)
	if err != nil {
		debugLog("调用上游失败: %v", err)
		httpErrorJSON(w, "Failed to call upstream", http.StatusBadGateway)
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
		httpErrorJSON(w, "Upstream error", http.StatusBadGateway)
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
					s = strings.ReplaceAll(s, "\n> ", "\n") // <--- 修正换行符
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

	// 构造完整响应，使用原始模型ID（OpenAI completions 风格：choices[0].message.content）
	response := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   originalModelID, // 使用原始模型ID
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
