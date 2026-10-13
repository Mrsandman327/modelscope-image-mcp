# modelscope-image-mcp

基于官方 [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) 的 **ModelScope Qwen-Image 文生图** MCP 服务,提供一个工具:

| 工具 | 调用 | 说明 |
|---|---|---|
| `modelscope_image_generate` | `POST {baseURL}/v1/images/generations` + `GET {baseURL}/v1/tasks/{task_id}` | 文生图:提交异步任务 → 轮询 → 下载图片到本地,返回文件路径 |

> ModelScope 生图是**异步任务式**接口:提交返回 `task_id` → 轮询任务状态 → 从 `output_images` 取图。

## 构建

```bash
go build -o modelscope-image-mcp.exe .
```

依赖:Go 1.25+,官方 go-sdk v1.7.0。编译后 `go vet ./...` 应无本项目代码告警。

## 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `MODELSCOPE_API_KEY` | 是 | - | ModelScope API Key(`ms-` 开头) |
| `MODELSCOPE_BASE_URL` | 否 | `https://api-inference.modelscope.cn` | API 基地址(填到域名即可,末尾 `/` 或误带的 `/v1` 会被自动去掉) |
| `MODELSCOPE_IMAGE_MODEL` | 是 | - | 模型 ID,例:`Qwen/Qwen-Image-2.1` |

缺失 `MODELSCOPE_API_KEY` 或 `MODELSCOPE_IMAGE_MODEL` 时服务启动即报错退出。

## 传输

支持 **stdio**(默认,供 MCP 客户端以本地进程方式拉起)和 **http**:

```bash
modelscope-image-mcp.exe                      # stdio
modelscope-image-mcp.exe -transport http -http-addr :8080
```

## opencode 接入

在 `opencode.jsonc` 的 `mcp.servers` 段**新增一项**:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "modelscope-image": {
        "type": "local",
        "disabled": false,
        "command": ["E:\\code\\test\\go\\mcp\\modelscope-image-mcp\\modelscope-image-mcp.exe"],
        "environment": {
          "MODELSCOPE_API_KEY": "ms-xxxx",
          "MODELSCOPE_IMAGE_MODEL": "Qwen/Qwen-Image-2.1"
        }
      }
    }
  }
}
```

地址有内置默认值,不需要配;要指向别处时再加 `MODELSCOPE_BASE_URL`。

保存后重启 opencode 生效。工具会以 `modelscope_image_generate` 出现在会话中。

> key 也可写成 `"{env:MODELSCOPE_API_KEY}"`,从 opencode 服务器的环境变量取,避免明文。

## 参数

`modelscope_image_generate(prompt, filename, model, size, seed, negative_prompt, save_dir, poll_interval, max_wait)`

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `prompt` | 是 | - | 图片描述 |
| `filename` | 否 | `qwen_image` | 文件名(不含扩展名) |
| `model` | 否 | 环境变量 `MODELSCOPE_IMAGE_MODEL` | 本次调用使用的模型 ID,可覆盖配置 |
| `size` | 否 | 服务端默认 | 如 `1024x1024` |
| `seed` | 否 | `0`(随机) | 随机种子 |
| `negative_prompt` | 否 | 空 | 负向提示词 |
| `save_dir` | 否 | 进程当前目录 | 保存目录 |
| `poll_interval` | 否 | `5` | 轮询间隔(秒) |
| `max_wait` | 否 | `300` | 最长等待(秒) |

生成成功后返回类似:

```
生成成功(model=Qwen/Qwen-Image-2.1, task=xxx),共 1 张图片:
E:\work\qwen_image.png
```

图片只落盘、不内联返回;需要查看时让模型 `read` 该路径即可。

## 手动测试(stdio)

```powershell
$env:MODELSCOPE_API_KEY = "ms-xxxx"
$env:MODELSCOPE_IMAGE_MODEL = "Qwen/Qwen-Image-2.1"
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' | .\modelscope-image-mcp.exe
```

## 实现说明

- 非类型化 handler(`server.AddTool` + `mcp.ToolHandler`),结果只发 `TextContent`,不发 `structuredContent`(opencode 客户端要求 structuredContent 必须是 record,字符串会导致校验失败)
- 错误统一转 `IsError: true` 的 result,LLM 可直接看到错误信息
- 三步调用:提交(带 `X-ModelScope-Async-Mode: true`)→ 轮询(带 `X-ModelScope-Task-Type: image_generation`)→ 下载 `output_images`
- 分阶段超时:提交 120s / 单次轮询 60s / 下载 120s;任务总等待由参数 `max_wait` 控制
- 下载时按 `Content-Type` 决定扩展名(`.jpg` / `.png`)
- 文件名经 `filepath.Base` 清洗,避免路径穿越
- `MODELSCOPE_BASE_URL` 会去掉末尾 `/` 和误带的 `/v1`,避免拼成 `/v1/v1/...`

## 常见错误

| 现象 | 原因 |
|---|---|
| `MODELSCOPE_API_KEY is required` | 没配 key |
| `MODELSCOPE_IMAGE_MODEL is required` | 没配模型 ID |
| `submit task returned 401` | key 无效 |
| `submit task returned 404` | 路径或模型 ID 不对 |
| `task ... ended with status FAILED` | 提示词被拦截,换词重试 |
| `task ... not finished within 300s` | 调大 `max_wait` |
