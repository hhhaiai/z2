# Z2 - Z.ai OpenAI兼容API代理

这是一个为Z.ai GLM-4.5模型提供OpenAI兼容API接口的高性能代理服务器。支持流式和非流式响应，具备完整的统计监控功能，无需配置即可运行。

## ✨ 主要功能

- 🔄 **OpenAI API兼容**: 完全兼容OpenAI的API格式，无需修改客户端代码
- 🌊 **流式响应支持**: 支持实时流式输出，提供更好的用户体验
- 📊 **实时统计**: 内置Web统计面板，实时监控API使用情况
- 🔐 **匿名访问**: 无需API密钥即可使用，自动获取匿名token
- 🚀 **零配置启动**: 无需任何环境变量即可运行
- 🐳 **Docker支持**: 提供Docker镜像，便于部署

## 🚀 快速开始

### 本地部署

1. **克隆仓库**
   ```bash
   git clone https://github.com/your-username/z2.git
   cd z2
   ```

2. **直接运行**
   ```bash
   go run main.go
   ```
   
   或者编译后运行：
   ```bash
   go build -o main main.go
   ./main
   ```

3. **验证服务**
   ```bash
   # 检查模型列表
   curl http://localhost:7860/v1/models
   
   # 测试聊天接口
   curl -X POST http://localhost:7860/v1/chat/completions \
     -H "Content-Type: application/json" \
     -d '{"model": "GLM-4.5", "messages": [{"role": "user", "content": "你好"}]}'
   ```

4. **访问统计面板**
   ```
   http://localhost:7860/stats
   ```

### Docker部署

**本地构建：**
```bash
# 构建并运行
docker build -t z2-api .
docker run -p 7860:7860 z2-api
```

**使用预构建镜像：**
```bash
# 使用GitHub Container Registry的镜像
docker run -p 7860:7860 ghcr.io/your-username/z2:latest
```

**GitHub Actions自动构建：**
- 推送tag时自动构建多架构Docker镜像（amd64、arm64、armv7）
- 镜像发布到GitHub Container Registry
- 支持版本标签和latest标签

## 📖 使用方法

### Python示例

```python
import openai

# 配置客户端（无需API密钥）
client = openai.OpenAI(
    api_key="sk-any-key",  # 可以是任意值
    base_url="http://localhost:7860/v1"
)

# 非流式请求
response = client.chat.completions.create(
    model="GLM-4.5",
    messages=[{"role": "user", "content": "你好，请介绍一下自己"}]
)
print(response.choices[0].message.content)

# 流式请求
response = client.chat.completions.create(
    model="GLM-4.5",
    messages=[{"role": "user", "content": "请写一首关于春天的诗"}],
    stream=True
)
for chunk in response:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="")
```

### curl示例

```bash
# 获取模型列表
curl http://localhost:7860/v1/models

# 非流式聊天
curl -X POST http://localhost:7860/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'

curl -X POST http://localhost:7860/api/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'

# 流式聊天
curl -X POST http://localhost:7860/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'

curl -X POST http://localhost:7860/api/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'

```

### JavaScript示例

```javascript
async function chatWithGLM(message) {
  const response = await fetch('http://localhost:7860/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model: 'GLM-4.5',
      messages: [{ role: 'user', content: message }]
    })
  });
  
  const data = await response.json();
  console.log(data.choices[0].message.content);
}

chatWithGLM('你好，请介绍一下JavaScript');
```