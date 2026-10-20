# modelscope-image-mcp

[![M8ven Score](https://m8ven.ai/badge/mcp/mrsandman327-modelscope-image-mcp-34rvrm?v=b05762cd40329d833bd9a87e21f1bc31)](https://m8ven.ai/mcp/mrsandman327-modelscope-image-mcp-34rvrm?s=readme)

基于官方 [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) 的 [ModelScope](https://www.modelscope.cn) 生图模型**文生图** MCP 服务,提供一个工具:

| 工具 | 说明 |
|---|---|
| `modelscope_image_generate` | 用 生图模型 文生图:提交异步任务 → 轮询 → 下载图片到本地,返回文件路径 |

参数:

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `prompt` | 是 | - | 图片描述 |
| `filename` | 否 | `qwen_image` | 文件名(不含扩展名) |
| `model` | 否 | 环境变量 `MODELSCOPE_IMAGE_MODEL` | 本次调用用的模型 ID,可覆盖配置 |
| `size` | 否 | 服务端默认 | 如 `1024x1024` |
| `seed` | 否 | `0`(随机) | 随机种子 |
| `negative_prompt` | 否 | 空 | 负向提示词 |
| `save_dir` | 否 | 进程当前目录 | 保存目录 |
| `poll_interval` | 否 | `5` | 轮询间隔(秒) |
| `max_wait` | 否 | `300` | 最长等待(秒) |

生成成功返回落盘路径,例如:

```
生成成功(model=Qwen/Qwen-Image-2.1-Turbo, task=xxx),共 1 张图片:
E:\work\qwen_image.png
```

图片只落盘、不内联返回;需要查看时让模型 `read` 该路径即可。

## 构建

```bash
go build -o modelscope-image-mcp.exe .
```

依赖:Go 1.25+,官方 go-sdk v1.7.0。编译后 `go vet ./...` 应无本项目代码告警。

## 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `MODELSCOPE_API_KEY` | 是 | - | ModelScope API Key(`ms-` 开头) |
| `MODELSCOPE_BASE_URL` | 否 | `https://api-inference.modelscope.cn` | API 基地址(填到域名即可,末尾 `/` 或误带的 `/v1` 会自动去掉) |
| `MODELSCOPE_IMAGE_MODEL` | 是 | - | 模型 ID,例:`Qwen/Qwen-Image-2.1` |

缺失 `MODELSCOPE_API_KEY` 或 `MODELSCOPE_IMAGE_MODEL` 时服务启动即报错退出。

## 调用流程

ModelScope 生图是**异步任务式**接口,三步走:

```
① POST {MODELSCOPE_BASE_URL}/v1/images/generations
     header: Authorization: Bearer <key>
             Content-Type: application/json
             X-ModelScope-Async-Mode: true          ← 声明异步模式,立即返回 task_id
     body:   {"model": "...", "prompt": "...", "size": "...", "seed": ..., "negative_prompt": "..."}
     → {"task_id": "..."}

② GET  {MODELSCOPE_BASE_URL}/v1/tasks/{task_id}
     header: Authorization: Bearer <key>
             X-ModelScope-Task-Type: image_generation   ← 声明查的是图像生成类任务
     → {"task_status": "SUCCEED", "output_images": ["https://...png", ...]}

③ 下载 output_images 里的图片到本地,按 Content-Type 决定 .png / .jpg
```

轮询间隔默认 5s,总等待默认 300s;状态出现 `FAILED` / `CANCELED` 立即报错,其余状态持续轮询到超时。

## MCP 客户端接入

### opencode

在 opencode 配置(`opencode.jsonc`,全局或项目级)的 `mcp.servers` 段添加:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "modelscope-image": {
        "type": "local",
        "disabled": false,
        "command": ["E:/mcp/modelscope-image-mcp.exe"],
        "environment": {
          "MODELSCOPE_API_KEY": "ms-xxxx",
          "MODELSCOPE_IMAGE_MODEL": "Qwen/Qwen-Image-2.1-Turbo"
        }
      }
    }
  }
}
```

地址有内置默认值,不需要配;要指向别处再加 `MODELSCOPE_BASE_URL`。保存后重启 opencode 生效。

### Claude Code

项目级配置放根目录 `.mcp.json`(随仓库共享);用户级配置放 `~/.claude.json` 的 `mcpServers` 键下。`command` 是**字符串**(非数组),`env` 支持 `${VAR}` 展开(从 shell 环境取):

```json
{
  "mcpServers": {
    "modelscope-image": {
      "type": "stdio",
      "command": "E:/mcp/modelscope-image-mcp.exe",
      "args": [],
      "env": {
        "MODELSCOPE_API_KEY": "ms-xxxx",
        "MODELSCOPE_IMAGE_MODEL": "Qwen/Qwen-Image-2.1-Turbo"
      }
    }
  }
}
```

也可以用 CLI 添加:`claude mcp add modelscope-image -- E:/code/test/go/mcp/modelscope-image-mcp/modelscope-image-mcp.exe`,之后 `claude mcp get modelscope-image` 查看。修改后重启 Claude Code 生效。

### OpenAI Codex CLI

配置在 `~/.codex/config.toml`,`[mcp_servers.<名字>]` 段,`command` 为字符串,环境变量用 `env` 表:

```toml
[mcp_servers.modelscope-image]
command = "E:/mcp/modelscope-image-mcp.exe"
args = []
env = { MODELSCOPE_API_KEY = "ms-xxxx", MODELSCOPE_IMAGE_MODEL = "Qwen/Qwen-Image-2.1" }
```

也可以用 CLI 添加:`codex mcp add modelscope-image -- E:/code/test/go/mcp/modelscope-image-mcp/modelscope-image-mcp.exe`(加 `--scope project` 则写入项目级配置)。修改后重启 Codex 生效。

> 三个客户端差异速记:opencode 用 `command` 数组 + `environment`;Claude Code 用 `command` 字符串 + `env`;Codex 用 TOML 段 + `env` 表。都通过 stdio 拉起本进程,环境变量注入 key 与模型 ID。

## 手动测试(stdio)

```powershell
$env:MODELSCOPE_API_KEY = "ms-xxxx"
$env:MODELSCOPE_IMAGE_MODEL = "Qwen/Qwen-Image-2.1"
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' | .\modelscope-image-mcp.exe
```

## 传输

支持 **stdio**(默认,三个客户端都用这种)和 **http**:

```bash
modelscope-image-mcp.exe                                      # stdio
modelscope-image-mcp.exe -transport http -http-addr :8080     # http (StreamableHTTP)
```

## 实现说明

- 非类型化 handler(`server.AddTool` + `mcp.ToolHandler`),结果只发 `TextContent`,不发 `structuredContent`(opencode 客户端要求 record,字符串会导致校验失败)
- 错误统一转 `IsError: true` result,LLM 可直接看到错误信息
- 分阶段超时:提交 120s / 单次轮询 60s / 下载 120s;任务总等待由 `max_wait` 控制
- 文件名经 `filepath.Base` 清洗,避免路径穿越
- `MODELSCOPE_BASE_URL` 会去掉末尾 `/` 和误带的 `/v1`,避免拼成 `/v1/v1/...`

## 常见错误

| 现象 | 原因 |
|---|---|
| `MODELSCOPE_API_KEY is required` | 没配 key(客户端 `environment`/`env` 没注入) |
| `MODELSCOPE_IMAGE_MODEL is required` | 没配模型 ID |
| `submit task returned 401` | key 无效 |
| `submit task returned 404` | 路径或模型 ID 不对 |
| `task ... ended with status FAILED` | 提示词被拦截,换词重试 |
| `task ... not finished within 300s` | 调大 `max_wait` |

## 已知限制

- 图片只落盘、不内联返回:客户端无法直接渲染,需要模型再 `read` 一次该路径才能看图
- 保存目录默认是 **MCP 进程的工作目录**(由客户端决定),想固定位置请在调用时传 `save_dir`
- `output_images` 里的链接是临时地址,任务完成后需尽快下载(本工具已在任务成功时立即下载)
