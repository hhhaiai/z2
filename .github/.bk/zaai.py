#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""
Z.AI API 客户端封装
完整实现 Z.AI 聊天API的所有功能，包括认证、签名、流式/非流式请求

依赖：
    - requests (标准HTTP库)
    - 标准库: json, time, uuid, hmac, hashlib, base64, urllib

使用示例见文件底部 if __name__ == "__main__"
"""

import json
import time
import uuid
import hmac
import hashlib
import base64
import requests
from datetime import datetime
from urllib.parse import urlencode
from typing import Dict, List, Any, Optional, Generator, Union


class ZAIClient:
    """Z.AI API 客户端"""
    
    # 模型映射
    MODEL_MAPPING = {
        "GLM-4.5": "0727-360B-API",
        "GLM-4.5-Thinking": "0727-360B-API",
        "GLM-4.5-Search": "0727-360B-API",
        "GLM-4.5-Air": "0727-106B-API",
        "GLM-4.6": "GLM-4-6-API-V1",
        "GLM-4.6-Thinking": "GLM-4-6-API-V1",
        "GLM-4.6-Search": "GLM-4-6-API-V1",
    }
    
    def __init__(self, token: Optional[str] = None, anonymous: bool = True):
        """
        初始化 Z.AI 客户端
        
        Args:
            token: 用户认证token（可选），如果提供则使用认证模式
            anonymous: 是否使用匿名模式（默认True），匿名模式会自动获取访客token
        """
        self.base_url = "https://chat.z.ai"
        self.api_url = f"{self.base_url}/api/chat/completions"
        self.auth_url = f"{self.base_url}/api/v1/auths/"
        self.token = token
        self.anonymous = anonymous
        self.signing_secret = "junjie"  # Z.AI的默认签名密钥
        
    def get_guest_token(self) -> str:
        """
        获取访客令牌（匿名模式）
        
        Returns:
            str: 访客token
            
        Raises:
            Exception: 获取token失败
        """
        headers = {
            "Accept": "*/*",
            "Accept-Language": "zh-CN,zh;q=0.9",
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "Referer": f"{self.base_url}/",
        }
        
        try:
            response = requests.get(self.auth_url, headers=headers, timeout=10)
            if response.status_code == 200:
                data = response.json()
                token = data.get("token", "")
                if token:
                    return token
            raise Exception(f"获取访客token失败: {response.status_code}")
        except Exception as e:
            raise Exception(f"获取访客token异常: {e}")
    
    def _get_token(self) -> str:
        """获取有效的token"""
        if self.token:
            return self.token
        if self.anonymous:
            return self.get_guest_token()
        raise Exception("未配置token且未启用匿名模式")
    
    def _generate_uuid(self) -> str:
        """生成UUID"""
        return str(uuid.uuid4())
    
    def _extract_user_id_from_token(self, token: str) -> str:
        """从JWT token中提取user_id"""
        try:
            parts = token.split(".")
            if len(parts) < 2:
                return "guest"
            
            # Base64解码payload
            payload_raw = parts[1]
            padding = "=" * (-len(payload_raw) % 4)
            payload_bytes = base64.urlsafe_b64decode(payload_raw + padding)
            payload = json.loads(payload_bytes.decode("utf-8", errors="ignore"))
            
            # 尝试多个可能的user_id字段
            for key in ("id", "user_id", "uid", "sub"):
                val = payload.get(key)
                if val:
                    return str(val)
            return "guest"
        except:
            return "guest"
    
    def _generate_signature(
        self, 
        message_text: str, 
        request_id: str, 
        timestamp_ms: int, 
        user_id: str
    ) -> str:
        """
        生成双层HMAC-SHA256签名
        
        Layer1: derived_key = HMAC(secret, window_index)
        Layer2: signature = HMAC(derived_key, canonical_string)
        
        Args:
            message_text: 消息文本
            request_id: 请求ID
            timestamp_ms: 时间戳（毫秒）
            user_id: 用户ID
            
        Returns:
            str: 签名hex字符串
        """
        # 计算时间窗口索引（5分钟窗口）
        window_index = timestamp_ms // (5 * 60 * 1000)
        
        # Layer1: 派生密钥
        root_key = self.signing_secret.encode("utf-8")
        derived_hex = hmac.new(
            root_key, 
            str(window_index).encode("utf-8"), 
            hashlib.sha256
        ).hexdigest()
        
        # Layer2: 生成签名
        canonical_string = (
            f"requestId,{request_id},"
            f"timestamp,{timestamp_ms},"
            f"user_id,{user_id}|{message_text}|{timestamp_ms}"
        )
        signature = hmac.new(
            derived_hex.encode("utf-8"),
            canonical_string.encode("utf-8"),
            hashlib.sha256
        ).hexdigest()
        
        return signature
    
    def _build_headers(self, token: str, chat_id: str, signature: str) -> Dict[str, str]:
        """构建请求头"""
        return {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "Accept-Language": "zh-CN,zh;q=0.9",
            "Authorization": f"Bearer {token}",
            "X-Signature": signature,
            "X-FE-Version": "prod-fe-1.0.79",
            "Origin": self.base_url,
            "Referer": f"{self.base_url}/c/{chat_id}",
        }
    
    def _build_request_body(
        self,
        messages: List[Dict[str, str]],
        model: str,
        chat_id: str,
        **kwargs
    ) -> Dict[str, Any]:
        """构建请求体"""
        # 获取上游模型ID
        upstream_model_id = self.MODEL_MAPPING.get(model, "0727-360B-API")
        
        # 判断模型特性
        is_thinking = "thinking" in model.lower()
        is_search = "search" in model.lower()
        
        # 构建MCP服务器列表
        mcp_servers = []
        if is_search and "4.5" in model:
            mcp_servers.append("deep-web-search")
        
        # 构建请求体
        body = {
            "stream": kwargs.get("stream", True),
            "model": upstream_model_id,
            "messages": messages,
            "params": {},
            "features": {
                "image_generation": False,
                "web_search": is_search,
                "auto_web_search": is_search,
                "preview_mode": False,
                "flags": [],
                "features": [],
                "enable_thinking": is_thinking,
            },
            "background_tasks": {
                "title_generation": False,
                "tags_generation": False,
            },
            "mcp_servers": mcp_servers,
            "variables": {
                "{{USER_NAME}}": "Guest",
                "{{USER_LOCATION}}": "Unknown",
                "{{CURRENT_DATETIME}}": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
                "{{CURRENT_DATE}}": datetime.now().strftime("%Y-%m-%d"),
                "{{CURRENT_TIME}}": datetime.now().strftime("%H:%M:%S"),
                "{{CURRENT_WEEKDAY}}": datetime.now().strftime("%A"),
                "{{CURRENT_TIMEZONE}}": "Asia/Shanghai",
                "{{USER_LANGUAGE}}": "zh-CN",
            },
            "model_item": {
                "id": upstream_model_id,
                "name": model,
                "owned_by": "z.ai"
            },
            "chat_id": chat_id,
            "id": self._generate_uuid(),
        }
        
        # 添加可选参数
        if "temperature" in kwargs:
            body["params"]["temperature"] = kwargs["temperature"]
        if "max_tokens" in kwargs:
            body["params"]["max_tokens"] = kwargs["max_tokens"]
        if "tools" in kwargs and not is_thinking:
            body["tools"] = kwargs["tools"]
        
        return body
    
    def chat(
        self,
        messages: List[Dict[str, str]],
        model: str = "GLM-4.6",
        stream: bool = True,
        **kwargs
    ) -> Union[Generator[str, None, None], Dict[str, Any]]:
        """
        发送聊天请求
        
        Args:
            messages: 消息列表，格式：[{"role": "user", "content": "你好"}]
            model: 模型名称，支持：GLM-4.5、GLM-4.5-Thinking、GLM-4.5-Search等
            stream: 是否流式输出（默认True）
            **kwargs: 其他参数
                - temperature: 温度参数
                - max_tokens: 最大token数
                - tools: 工具列表（OpenAI格式）
                
        Returns:
            流式模式: Generator，每次yield一个SSE格式的字符串
            非流式模式: Dict，完整的响应数据
            
        Example:
            # 流式调用
            for chunk in client.chat([{"role": "user", "content": "你好"}]):
                print(chunk, end="")
            
            # 非流式调用
            response = client.chat(
                [{"role": "user", "content": "你好"}],
                stream=False
            )
            print(response)
        """
        # 获取token
        token = self._get_token()
        
        # 生成chat_id和其他参数
        chat_id = self._generate_uuid()
        request_id = self._generate_uuid()
        timestamp_ms = int(time.time() * 1000)
        user_id = self._extract_user_id_from_token(token)
        
        # 提取最后一条用户消息用于签名
        last_user_message = ""
        for msg in reversed(messages):
            if msg.get("role") == "user":
                last_user_message = msg.get("content", "")
                break
        
        # 生成签名
        signature = self._generate_signature(
            last_user_message,
            request_id,
            timestamp_ms,
            user_id
        )
        
        # 构建请求
        headers = self._build_headers(token, chat_id, signature)
        body = self._build_request_body(messages, model, chat_id, stream=stream, **kwargs)
        
        # 构建URL（带查询参数）
        query_params = {
            "timestamp": timestamp_ms,
            "requestId": request_id,
            "user_id": user_id,
            "token": token,
            "current_url": f"{self.base_url}/c/{chat_id}",
            "pathname": f"/c/{chat_id}",
            "signature_timestamp": timestamp_ms,
        }
        url = f"{self.api_url}?{urlencode(query_params)}"
        
        # 发送请求
        if stream:
            return self._stream_request(url, headers, body)
        else:
            return self._non_stream_request(url, headers, body)
    
    def _stream_request(
        self, 
        url: str, 
        headers: Dict[str, str], 
        body: Dict[str, Any]
    ) -> Generator[str, None, None]:
        """流式请求 - 修复了中文乱码问题"""
        response = requests.post(
            url,
            headers=headers,
            json=body,
            stream=True,
            timeout=60
        )
        
        if response.status_code != 200:
            raise Exception(f"请求失败: {response.status_code}")
        
        for line in response.iter_lines(decode_unicode=False):  # 不自动解码
            if line:
                try:
                    # 手动解码并确保中文正确显示
                    decoded_line = line.decode('utf-8')
                    yield decoded_line + "\n"
                except UnicodeDecodeError:
                    # 如果utf-8解码失败，尝试其他常见编码
                    try:
                        decoded_line = line.decode('latin-1')
                        yield decoded_line + "\n"
                    except:
                        # 无法解码时，跳过该行或返回原始内容
                        pass
    
    def _non_stream_request(
        self, 
        url: str, 
        headers: Dict[str, str], 
        body: Dict[str, Any]
    ) -> Dict[str, Any]:
        """非流式请求（聚合流式响应）"""
        # Z.AI总是返回流式，需要聚合
        full_content = ""
        reasoning_content = ""
        
        for line in self._stream_request(url, headers, {**body, "stream": True}):
            if not line.startswith("data: "):
                continue
            
            data_str = line[6:].strip()
            if data_str in ["[DONE]", ""]:
                continue
            
            try:
                chunk = json.loads(data_str)
                if chunk.get("type") == "chat:completion":
                    data = chunk.get("data", {})
                    phase = data.get("phase")
                    
                    if phase == "thinking":
                        reasoning_content += data.get("delta_content", "")
                    elif phase == "answer":
                        full_content += data.get("delta_content", "")
            except:
                pass
        
        return {
            "content": full_content.strip(),
            "reasoning_content": reasoning_content.strip() if reasoning_content else None
        }

    def get_content_from_chunk(self, chunk: str) -> str:
        """
        从SSE格式的chunk中提取并清理内容
        
        Args:
            chunk: SSE格式的响应文本
            
        Returns:
            str: 提取并清理后的内容
        """
        if chunk.startswith("data: "):
            data_str = chunk[6:].strip()
            if data_str not in ["[DONE]", ""]:
                try:
                    parsed = json.loads(data_str)
                    if parsed.get("type") == "chat:completion":
                        data = parsed.get("data", {})
                        return data.get("delta_content", "")
                except:
                    pass
        return ""


# =============================================================================
# 使用示例
# =============================================================================

if __name__ == "__main__":
    # 示例1: 匿名模式的基本对话
    print("=" * 60)
    print("示例1: 匿名模式的基本对话（流式）")
    print("=" * 60)
    
    client = ZAIClient(anonymous=True)
    
    messages = [
        {"role": "user", "content": "你好，请用一句话介绍一下自己"}
    ]
    
    print("用户: 你好，请用一句话介绍一下自己")
    print("助手: ", end="", flush=True)
    
    # 使用get_content_from_chunk方法提取内容并避免乱码
    for chunk in client.chat(messages):
        content = client.get_content_from_chunk(chunk)
        if content:
            print(content, end="", flush=True)
    
    print("\n\n示例执行完毕！")