package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "modelscope-image-mcp"
	serverVersion = "v1.0.0"

	defaultBaseURL = "https://api-inference.modelscope.cn"

	submitTimeout   = 120 * time.Second
	pollTimeout     = 60 * time.Second
	downloadTimeout = 120 * time.Second

	defaultPollInterval = 5   // 秒
	defaultMaxWait      = 300 // 秒

	maxJSONBytes  = 1 << 20  // 1 MiB
	maxImageBytes = 64 << 20 // 64 MiB

	// 异步任务式生图接口的协议头。这是接口本身的要求,与命名无关;
	// 同步(OpenAI 兼容)的 /images/generations 接口不需要它们。
	headerAsyncMode = "X-ModelScope-Async-Mode"
	headerTaskType  = "X-ModelScope-Task-Type"
	taskTypeImage   = "image_generation"
)

// ---------- 配置:从环境变量读取(MCP 客户端 environment 字段注入) ----------

type config struct {
	apiKey  string
	baseURL string
	model   string
}

func configFromEnv() (config, error) {
	c := config{
		apiKey:  strings.TrimSpace(os.Getenv("MODELSCOPE_API_KEY")),
		baseURL: strings.TrimSpace(os.Getenv("MODELSCOPE_BASE_URL")),
		model:   strings.TrimSpace(os.Getenv("MODELSCOPE_IMAGE_MODEL")),
	}
	if c.apiKey == "" {
		return config{}, errors.New("MODELSCOPE_API_KEY is required (set it in the MCP client environment)")
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.model == "" {
		return config{}, errors.New("MODELSCOPE_IMAGE_MODEL is required (set it in the MCP client environment)")
	}
	// 规范化:去掉末尾的 "/",以及误带的 "/v1"(请求路径里已含 /v1)
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	c.baseURL = strings.TrimSuffix(c.baseURL, "/v1")
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	return c, nil
}

// ---------- 参数辅助 ----------

func unmarshalArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	if req.Params == nil || len(req.Params.Arguments) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	return args, nil
}

func argString(args map[string]any, name string, def string) (string, error) {
	v, ok := args[name]
	if !ok || v == nil {
		return def, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	return s, nil
}

func argRequiredString(args map[string]any, name string) (string, error) {
	s, err := argString(args, name, "")
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	return s, nil
}

// argInt 返回可选整数参数,缺失时用 def,超出 [lo,hi] 报错。
func argInt(args map[string]any, name string, def, lo, hi int) (int, error) {
	v, ok := args[name]
	if !ok || v == nil {
		return def, nil
	}
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("argument %q must be an integer", name)
	}
	n := int(f)
	if float64(n) != f {
		return 0, fmt.Errorf("argument %q must be an integer", name)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("argument %q must be between %d and %d", name, lo, hi)
	}
	return n, nil
}

// ---------- 结果构造(非类型化 handler,只发 TextContent) ----------

func okResult(parts ...string) *mcp.CallToolResult {
	content := make([]mcp.Content, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		content = append(content, &mcp.TextContent{Text: p})
	}
	return &mcp.CallToolResult{Content: content}
}

func errResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// ---------- 生图客户端 ----------
//
// 该接口是异步任务式,三步走:
//  1. POST {baseURL}/v1/images/generations (带异步模式头) -> task_id
//  2. GET  {baseURL}/v1/tasks/{task_id}    (带任务类型头) -> 轮询
//  3. 状态 SUCCEED 时,从 output_images 下载图片

type imageClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

type taskStatus struct {
	TaskID       string   `json:"task_id"`
	TaskStatus   string   `json:"task_status"`
	OutputImages []string `json:"output_images"`
	Message      string   `json:"message"`
}

// submitTask 提交异步生图任务,返回 task_id。
func (c *imageClient) submitTask(ctx context.Context, payload map[string]any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/images/generations", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build submit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerAsyncMode, "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("submit task failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return "", fmt.Errorf("read submit response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("submit task returned %s: %s", resp.Status, trimBody(body))
	}

	var out struct {
		TaskID  string `json:"task_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode submit response: %w (%s)", err, trimBody(body))
	}
	if out.TaskID == "" {
		return "", fmt.Errorf("submit response has no task_id: %s", trimBody(body))
	}
	return out.TaskID, nil
}

// waitTask 轮询任务直到成功/失败/超时。
func (c *imageClient) waitTask(ctx context.Context, taskID string, interval, maxWait int) (*taskStatus, error) {
	deadline := time.Now().Add(time.Duration(maxWait) * time.Second)
	for {
		st, err := c.pollTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		switch strings.ToUpper(st.TaskStatus) {
		case "SUCCEED":
			return st, nil
		case "FAILED", "CANCELED", "CANCELLED", "UNKNOWN":
			return nil, fmt.Errorf("task %s ended with status %s: %s", taskID, st.TaskStatus, trimBody([]byte(st.Message)))
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("task %s not finished within %ds (last status %s)", taskID, maxWait, st.TaskStatus)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
}

func (c *imageClient) pollTask(ctx context.Context, taskID string) (*taskStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/tasks/"+taskID, nil)
	if err != nil {
		return nil, fmt.Errorf("build poll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set(headerTaskType, taskTypeImage)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll task failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
	if err != nil {
		return nil, fmt.Errorf("read poll response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("poll task returned %s: %s", resp.Status, trimBody(body))
	}

	var st taskStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decode poll response: %w (%s)", err, trimBody(body))
	}
	return &st, nil
}

// download 下载单张图片到 dir/name+扩展名,返回落盘路径。
func (c *imageClient) download(ctx context.Context, url, dir, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}

	ext := ".png"
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "jpeg") || strings.Contains(ct, "jpg") {
		ext = ".jpg"
	}
	path := filepath.Join(dir, name+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// ---------- 工具:modelscope_image_generate ----------

func generateImageHandler(c *imageClient, cfg config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := unmarshalArgs(req)
		if err != nil {
			return errResult(err.Error()), nil
		}

		prompt, err := argRequiredString(args, "prompt")
		if err != nil {
			return errResult(err.Error()), nil
		}
		filename, err := argString(args, "filename", "qwen_image")
		if err != nil {
			return errResult(err.Error()), nil
		}
		model, err := argString(args, "model", cfg.model)
		if err != nil {
			return errResult(err.Error()), nil
		}
		size, err := argString(args, "size", "")
		if err != nil {
			return errResult(err.Error()), nil
		}
		negative, err := argString(args, "negative_prompt", "")
		if err != nil {
			return errResult(err.Error()), nil
		}
		saveDir, err := argString(args, "save_dir", "")
		if err != nil {
			return errResult(err.Error()), nil
		}
		seed, err := argInt(args, "seed", 0, 0, 2147483647)
		if err != nil {
			return errResult(err.Error()), nil
		}
		pollInterval, err := argInt(args, "poll_interval", defaultPollInterval, 1, 60)
		if err != nil {
			return errResult(err.Error()), nil
		}
		maxWait, err := argInt(args, "max_wait", defaultMaxWait, 5, 1800)
		if err != nil {
			return errResult(err.Error()), nil
		}

		payload := map[string]any{"model": model, "prompt": prompt}
		if size != "" {
			payload["size"] = size
		}
		if seed > 0 {
			payload["seed"] = seed
		}
		if negative != "" {
			payload["negative_prompt"] = negative
		}

		taskID, err := c.submitTask(ctx, payload)
		if err != nil {
			return errResult(err.Error()), nil
		}

		st, err := c.waitTask(ctx, taskID, pollInterval, maxWait)
		if err != nil {
			return errResult(err.Error()), nil
		}
		if len(st.OutputImages) == 0 {
			return errResult(fmt.Sprintf("task %s succeeded but returned no image (message: %s)", taskID, st.Message)), nil
		}

		dir := saveDir
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				wd = "."
			}
			dir = wd
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errResult(fmt.Sprintf("create save_dir %q: %v", dir, err)), nil
		}

		base := filepath.Base(filename)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if base == "" || base == "." {
			base = "qwen_image"
		}

		saved := make([]string, 0, len(st.OutputImages))
		for i, u := range st.OutputImages {
			name := base
			if len(st.OutputImages) > 1 {
				name = fmt.Sprintf("%s_%d", base, i)
			}
			p, err := c.download(ctx, u, dir, name)
			if err != nil {
				return errResult(fmt.Sprintf("download image %d failed: %v", i, err)), nil
			}
			saved = append(saved, p)
		}

		return okResult(
			fmt.Sprintf("生成成功(model=%s, task=%s),共 %d 张图片:", model, taskID, len(saved)),
			strings.Join(saved, "\n"),
		), nil
	}
}

var generateImageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"prompt":          map[string]any{"type": "string", "description": "image description / prompt (required)"},
		"filename":        map[string]any{"type": "string", "description": "output file name without extension, default qwen_image"},
		"model":           map[string]any{"type": "string", "description": "model id to send; defaults to the configured MODELSCOPE_IMAGE_MODEL"},
		"size":            map[string]any{"type": "string", "description": "image size like 1024x1024; omit for server default"},
		"seed":            map[string]any{"type": "integer", "description": "random seed; 0 = random"},
		"negative_prompt": map[string]any{"type": "string", "description": "negative prompt"},
		"save_dir":        map[string]any{"type": "string", "description": "directory to save images into, default current working directory"},
		"poll_interval":   map[string]any{"type": "integer", "description": "task poll interval in seconds, default 5"},
		"max_wait":        map[string]any{"type": "integer", "description": "max seconds to wait for the task, default 300"},
	},
	"required": []string{"prompt"},
}

// ---------- 入口 ----------

func main() {
	transport := flag.String("transport", "stdio", "transport: stdio or http")
	httpAddr := flag.String("http-addr", ":8080", "HTTP listen address (http transport only)")
	flag.Parse()

	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	client := &imageClient{
		apiKey:  cfg.apiKey,
		baseURL: cfg.baseURL,
		http:    &http.Client{},
	}

	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "modelscope_image_generate",
		Description: "Generate an image with a Qwen image model (async task + polling) and save it to disk. Args: prompt (required), filename, model, size, seed, negative_prompt, save_dir, poll_interval, max_wait. Returns the saved file path(s).",
		InputSchema: generateImageSchema,
	}, generateImageHandler(client, cfg))

	switch *transport {
	case "stdio":
		log.Println(serverName + " starting (stdio)")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
	case "http":
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		log.Printf("%s starting (http) on %s", serverName, *httpAddr)
		if err := http.ListenAndServe(*httpAddr, handler); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown transport %q (must be stdio or http)", *transport)
	}
}
