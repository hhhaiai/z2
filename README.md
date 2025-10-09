# Z2

## 🚀 快速开始


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
