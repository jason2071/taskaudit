// Package main implements taskaudit - a CLI tool that scans a Go project
// directory and asks Claude to verify which checklist items are complete.
//
// Usage:
//
//	taskaudit -dir ./my-project -task "Planogram compare API" -checklist checklist.txt
//
// Requires: ANTHROPIC_API_KEY environment variable
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxTokens    = 4000
	apiTimeout   = 120 * time.Second
	maxFileBytes = 100 * 1024 // 100KB per file - skip ที่ใหญ่เกิน
)

// Provider configurations
type provider struct {
	name       string
	apiURL     string
	envKey     string
	defaultMod string
}

var providers = map[string]provider{
	"anthropic": {
		name:       "anthropic",
		apiURL:     "https://api.anthropic.com/v1/messages",
		envKey:     "ANTHROPIC_API_KEY",
		defaultMod: "claude-sonnet-4-20250514",
	},
	"openai": {
		name:       "openai",
		apiURL:     "https://api.openai.com/v1/chat/completions",
		envKey:     "OPENAI_API_KEY",
		defaultMod: "gpt-4o",
	},
	"gemini": {
		name:       "gemini",
		apiURL:     "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		envKey:     "GEMINI_API_KEY",
		defaultMod: "gemini-2.5-flash",
	},
	"openrouter": {
		name:       "openrouter",
		apiURL:     "https://openrouter.ai/api/v1/chat/completions",
		envKey:     "OPENROUTER_API_KEY",
		defaultMod: "anthropic/claude-sonnet-4",
	},
}

// Default folders ที่จะสแกนตาม Go clean architecture layout
var defaultIncludeDirs = []string{
	"internal/handler",
	"internal/service",
	"internal/repository",
	"internal/models",
	"internal/middleware",
}

// File patterns ที่จะดึงเข้ามาให้ AI วิเคราะห์
var defaultExtensions = []string{".go"}

// Folders/patterns ที่ skip
var skipPatterns = []string{
	"vendor", "node_modules", ".git", "tmp", "dist", "build",
}

type config struct {
	rootDir       string
	taskTitle     string
	taskDesc      string
	checklistPath string
	includePaths  string
	includeTests  bool
	jsonOutput    bool
	htmlPath      string
	mdPath        string
	verbose       bool
	providerName  string
	modelName     string
}

type codeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type checklistItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Done     bool   `json:"done"`
}

type auditResult struct {
	Results []struct {
		StepID   string `json:"stepId"`
		Status   string `json:"status"` // done | missing | partial | not_applicable
		Evidence string `json:"evidence"`
	} `json:"results"`
	MissingItems []struct {
		Title    string `json:"title"`
		Category string `json:"category"`
		Severity string `json:"severity"`
		Reason   string `json:"reason"`
	} `json:"missingItems"`
	Summary string `json:"summary"`
}

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func main() {
	cfg := parseFlags()

	if err := run(context.Background(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() *config {
	cfg := &config{}
	flag.StringVar(&cfg.rootDir, "dir", ".", "Root directory of project to scan")
	flag.StringVar(&cfg.taskTitle, "task", "", "Task title (required)")
	flag.StringVar(&cfg.taskDesc, "desc", "", "Task description")
	flag.StringVar(&cfg.checklistPath, "checklist", "", "Path to checklist file (one item per line, format: 'category: title')")
	flag.StringVar(&cfg.includePaths, "include", "", "Comma-separated extra paths (default: internal/handler,internal/service,internal/repository,internal/models)")
	flag.BoolVar(&cfg.includeTests, "tests", true, "Include _test.go files")
	flag.BoolVar(&cfg.jsonOutput, "json", false, "Output as JSON")
	flag.StringVar(&cfg.htmlPath, "html", "", "Export HTML report to path (e.g. -html ./audit.html)")
	flag.StringVar(&cfg.mdPath, "md", "", "Export Markdown report to path (e.g. -md ./audit.md)")
	flag.BoolVar(&cfg.verbose, "v", false, "Verbose output")
	flag.StringVar(&cfg.providerName, "provider", "anthropic", "AI provider: anthropic, openai, gemini, openrouter")
	flag.StringVar(&cfg.modelName, "model", "", "Model name (default: provider's default model)")
	flag.Parse()

	if cfg.taskTitle == "" {
		fmt.Fprintln(os.Stderr, "❌ -task is required")
		flag.Usage()
		os.Exit(1)
	}

	// Validate provider
	if _, ok := providers[cfg.providerName]; !ok {
		fmt.Fprintf(os.Stderr, "❌ unknown provider: %s (supported: anthropic, openai, gemini, openrouter)\n", cfg.providerName)
		os.Exit(1)
	}

	return cfg
}

func run(ctx context.Context, cfg *config) error {
	prov := providers[cfg.providerName]
	apiKey := os.Getenv(prov.envKey)
	if apiKey == "" {
		return fmt.Errorf("%s environment variable is required", prov.envKey)
	}

	// Resolve model name
	if cfg.modelName == "" {
		cfg.modelName = prov.defaultMod
	}

	// 1. Load checklist
	checklist, err := loadChecklist(cfg.checklistPath)
	if err != nil {
		return fmt.Errorf("load checklist: %w", err)
	}
	if cfg.verbose {
		fmt.Printf("📋 Loaded %d checklist items\n", len(checklist))
	}

	// 2. Scan code files
	files, err := scanFiles(cfg)
	if err != nil {
		return fmt.Errorf("scan files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no code files found in %s", cfg.rootDir)
	}
	if cfg.verbose {
		fmt.Printf("📂 Found %d code files\n", len(files))
		for _, f := range files {
			fmt.Printf("   - %s (%d bytes)\n", f.Path, len(f.Content))
		}
	}

	// 3. Call AI API
	fmt.Printf("🔍 Auditing code with %s (%s)...\n", cfg.providerName, cfg.modelName)
	result, err := auditCode(ctx, apiKey, cfg, checklist, files)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	// 4. Output
	if cfg.jsonOutput {
		return printJSON(result)
	}

	// Export HTML
	if cfg.htmlPath != "" {
		if err := exportHTML(cfg.htmlPath, cfg, checklist, result); err != nil {
			return fmt.Errorf("export html: %w", err)
		}
		fmt.Printf("📄 HTML report saved to %s\n", cfg.htmlPath)
	}

	// Export Markdown
	if cfg.mdPath != "" {
		if err := exportMarkdown(cfg.mdPath, cfg, checklist, result); err != nil {
			return fmt.Errorf("export markdown: %w", err)
		}
		fmt.Printf("📝 Markdown report saved to %s\n", cfg.mdPath)
	}

	// Always print to terminal too (unless -json)
	printReport(result, checklist)
	return nil
}

// loadChecklist reads checklist file with format: "category: title" per line.
// Empty file returns default Go backend checklist.
func loadChecklist(path string) ([]checklistItem, error) {
	if path == "" {
		return defaultChecklist(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var items []checklistItem
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		category, title := "code", line
		if len(parts) == 2 {
			category = strings.TrimSpace(parts[0])
			title = strings.TrimSpace(parts[1])
		}

		items = append(items, checklistItem{
			ID:       fmt.Sprintf("step-%d", i+1),
			Title:    title,
			Category: category,
		})
	}
	return items, nil
}

func defaultChecklist() []checklistItem {
	return []checklistItem{
		{ID: "1", Category: "code", Title: "สร้าง model (internal/models)"},
		{ID: "2", Category: "code", Title: "สร้าง repository layer"},
		{ID: "3", Category: "code", Title: "สร้าง service layer พร้อม business logic"},
		{ID: "4", Category: "code", Title: "สร้าง handler + routing"},
		{ID: "5", Category: "code", Title: "เพิ่ม validation (go-playground/validator)"},
		{ID: "6", Category: "code", Title: "Error handling ครบทุก layer"},
		{ID: "7", Category: "test", Title: "เขียน unit test (table-driven) สำหรับ service"},
		{ID: "8", Category: "test", Title: "Test error cases"},
		{ID: "9", Category: "docs", Title: "Comment สำคัญในที่จำเป็น"},
	}
}

// scanFiles walks the directory tree and collects relevant Go files.
// Returns empty slice if no files match.
func scanFiles(cfg *config) ([]codeFile, error) {
	includePaths := defaultIncludeDirs
	if cfg.includePaths != "" {
		includePaths = strings.Split(cfg.includePaths, ",")
		for i, p := range includePaths {
			includePaths[i] = strings.TrimSpace(p)
		}
	}

	files := make([]codeFile, 0, 32)

	err := filepath.Walk(cfg.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip ที่อ่านไม่ได้
		}

		// Skip directories ที่ไม่ต้องการ
		if info.IsDir() {
			name := info.Name()
			for _, skip := range skipPatterns {
				if name == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Check extension
		if !hasExtension(path, defaultExtensions) {
			return nil
		}

		// Check ว่าอยู่ใน include path มั้ย
		relPath, _ := filepath.Rel(cfg.rootDir, path)
		if !matchesAnyPath(relPath, includePaths) {
			return nil
		}

		// Skip test files ถ้าไม่ต้องการ
		if !cfg.includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}

		// Skip ไฟล์ที่ใหญ่เกินไป
		if info.Size() > maxFileBytes {
			if cfg.verbose {
				fmt.Printf("   ⚠ skip %s (%.1fKB > %dKB)\n", relPath, float64(info.Size())/1024, maxFileBytes/1024)
			}
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		files = append(files, codeFile{
			Path:    relPath,
			Content: string(content),
		})
		return nil
	})

	return files, err
}

func hasExtension(path string, exts []string) bool {
	ext := filepath.Ext(path)
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

func matchesAnyPath(relPath string, paths []string) bool {
	for _, p := range paths {
		if strings.HasPrefix(relPath, p) {
			return true
		}
	}
	return false
}

// auditCode calls AI provider API with the task context, checklist and code files,
// then parses the JSON response.
func auditCode(ctx context.Context, apiKey string, cfg *config, checklist []checklistItem, files []codeFile) (*auditResult, error) {
	prompt := buildPrompt(cfg, checklist, files)

	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	var text string
	var err error

	switch cfg.providerName {
	case "openai":
		text, err = callOpenAI(ctx, apiKey, cfg.modelName, prompt)
	case "openrouter":
		text, err = callOpenRouter(ctx, apiKey, cfg.modelName, prompt)
	case "gemini":
		text, err = callGemini(ctx, apiKey, cfg.modelName, prompt)
	default:
		text, err = callAnthropic(ctx, apiKey, cfg.modelName, prompt)
	}

	if err != nil {
		return nil, err
	}

	// Strip markdown code fences ถ้ามี
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result auditResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parse audit JSON: %w (text: %s)", err, text)
	}

	return &result, nil
}

// callAnthropic calls Claude Messages API
func callAnthropic(ctx context.Context, apiKey, modelName, prompt string) (string, error) {
	reqBody := apiRequest{
		Model:     modelName,
		MaxTokens: maxTokens,
		Messages:  []apiMessage{{Role: "user", Content: prompt}},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", providers["anthropic"].apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s - %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	var sb strings.Builder
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

// callOpenAI calls OpenAI Chat Completions API
func callOpenAI(ctx context.Context, apiKey, modelName, prompt string) (string, error) {
	type openAIMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type openAIReq struct {
		Model    string      `json:"model"`
		Messages []openAIMsg `json:"messages"`
	}
	type openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	reqBody := openAIReq{
		Model:    modelName,
		Messages: []openAIMsg{{Role: "user", Content: prompt}},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", providers["openai"].apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp openAIResp
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from OpenAI")
	}

	return apiResp.Choices[0].Message.Content, nil
}

// callGemini calls Google Gemini generateContent API
func callGemini(ctx context.Context, apiKey, modelName, prompt string) (string, error) {
	type geminiPart struct {
		Text string `json:"text"`
	}
	type geminiContent struct {
		Parts []geminiPart `json:"parts"`
	}
	type geminiReq struct {
		Contents []geminiContent `json:"contents"`
	}
	type geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	reqBody := geminiReq{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	apiURL := fmt.Sprintf(providers["gemini"].apiURL, modelName) + "?key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp geminiResp
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Candidates) == 0 {
		return "", fmt.Errorf("empty response from Gemini")
	}

	var sb strings.Builder
	for _, p := range apiResp.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String(), nil
}

// callOpenRouter calls OpenRouter API (OpenAI-compatible format, supports any model)
func callOpenRouter(ctx context.Context, apiKey, modelName, prompt string) (string, error) {
	type orMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type orReq struct {
		Model    string  `json:"model"`
		Messages []orMsg `json:"messages"`
	}
	type orResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	reqBody := orReq{
		Model:    modelName,
		Messages: []orMsg{{Role: "user", Content: prompt}},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", providers["openrouter"].apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp orResp
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from OpenRouter")
	}

	return apiResp.Choices[0].Message.Content, nil
}

func buildPrompt(cfg *config, checklist []checklistItem, files []codeFile) string {
	var sb strings.Builder

	sb.WriteString("คุณเป็น senior Go/Fiber code reviewer ตรวจ code ของ developer คนนี้แล้วเทียบกับ checklist ของงาน\n\n")
	sb.WriteString(fmt.Sprintf("งาน: %s\n", cfg.taskTitle))
	if cfg.taskDesc != "" {
		sb.WriteString(fmt.Sprintf("รายละเอียด: %s\n", cfg.taskDesc))
	}
	sb.WriteString("\nChecklist ทั้งหมด:\n")
	for _, item := range checklist {
		mark := " "
		if item.Done {
			mark = "✓"
		}
		sb.WriteString(fmt.Sprintf("%s: [%s] %s (%s)\n", item.ID, mark, item.Title, item.Category))
	}

	sb.WriteString("\nCode ที่ scan มาจาก project:\n")
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("\n--- FILE: %s ---\n%s\n", f.Path, f.Content))
	}

	sb.WriteString(`
ตรวจสอบทุก step ใน checklist แล้วบอกว่าใน code ทำหรือยัง:
- "done": ทำแล้วจริงใน code
- "missing": ยังไม่เห็นใน code (ขาด)
- "partial": ทำแล้วแต่ไม่สมบูรณ์ (เช่น มี handler แต่ไม่มี validation tag)
- "not_applicable": step ที่ไม่เกี่ยวกับ code (เช่น push PR, update SharePoint)

ตอบ JSON เท่านั้น ไม่มี markdown:
{
  "results": [
    {"stepId": "id", "status": "done|missing|partial|not_applicable", "evidence": "หลักฐานใน code หรือเหตุผล (ภาษาไทย สั้นๆ)"}
  ],
  "missingItems": [
    {"title": "สิ่งที่ขาดที่ developer อาจยังไม่ได้ใส่ใน checklist", "category": "code|test|docs", "severity": "high|medium|low", "reason": "เหตุผล"}
  ],
  "summary": "สรุป 2-3 ประโยค ภาษาไทย ว่า code อยู่ในสภาพไหน ขาดอะไรสำคัญ"
}`)

	return sb.String()
}

func printJSON(r *auditResult) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// ANSI color codes
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cRed    = "\033[31m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cGray   = "\033[90m"
	cCyan   = "\033[36m"
)

func printReport(r *auditResult, checklist []checklistItem) {
	titleByID := make(map[string]string, len(checklist))
	for _, c := range checklist {
		titleByID[c.ID] = c.Title
	}

	fmt.Println()
	fmt.Println(cBold + cCyan + "═══ CODE AUDIT REPORT ═══" + cReset)
	fmt.Println()
	fmt.Println(cBold + "📊 Summary:" + cReset)
	fmt.Println("   " + r.Summary)
	fmt.Println()

	// Counters
	counts := map[string]int{}
	for _, item := range r.Results {
		counts[item.Status]++
	}
	fmt.Println(cBold + "📈 Stats:" + cReset)
	fmt.Printf("   %s● done: %d%s   %s● missing: %d%s   %s● partial: %d%s   %s● n/a: %d%s\n\n",
		cGreen, counts["done"], cReset,
		cRed, counts["missing"], cReset,
		cYellow, counts["partial"], cReset,
		cGray, counts["not_applicable"], cReset)

	// Checklist results
	fmt.Println(cBold + "✓ Checklist Results:" + cReset)
	for _, item := range r.Results {
		title := titleByID[item.StepID]
		if title == "" {
			title = item.StepID
		}

		var icon, color string
		switch item.Status {
		case "done":
			icon, color = "✓", cGreen
		case "missing":
			icon, color = "✗", cRed
		case "partial":
			icon, color = "◐", cYellow
		case "not_applicable":
			icon, color = "—", cGray
		default:
			icon, color = "?", cGray
		}

		fmt.Printf("   %s%s%s %s\n", color, icon, cReset, title)
		if item.Evidence != "" {
			fmt.Printf("     %s%s%s\n", cGray, item.Evidence, cReset)
		}
	}
	fmt.Println()

	// Missing items
	if len(r.MissingItems) > 0 {
		fmt.Println(cBold + cYellow + "⚠ สิ่งที่ขาดเพิ่มเติม (ไม่อยู่ใน checklist):" + cReset)
		for _, item := range r.MissingItems {
			var sevColor string
			switch item.Severity {
			case "high":
				sevColor = cRed
			case "medium":
				sevColor = cYellow
			case "low":
				sevColor = cBlue
			default:
				sevColor = cGray
			}
			fmt.Printf("   %s[%s]%s %s\n", sevColor, strings.ToUpper(item.Severity), cReset, item.Title)
			fmt.Printf("     %s%s%s\n", cGray, item.Reason, cReset)
		}
		fmt.Println()
	}
}

// ─────────────────────────────────────────────────────────────────
// Export: HTML
// ─────────────────────────────────────────────────────────────────

// reportData is the view model passed to HTML and Markdown templates.
type reportData struct {
	Task         string
	Description  string
	GeneratedAt  string
	Summary      string
	Stats        map[string]int
	TotalSteps   int
	DonePercent  int
	Items        []reportItem
	MissingItems []reportMissing
}

type reportItem struct {
	Title    string
	Status   string // done | missing | partial | not_applicable
	Evidence string
	Category string
}

type reportMissing struct {
	Title    string
	Category string
	Severity string
	Reason   string
}

func buildReportData(cfg *config, checklist []checklistItem, r *auditResult) reportData {
	titleByID := make(map[string]string, len(checklist))
	catByID := make(map[string]string, len(checklist))
	for _, c := range checklist {
		titleByID[c.ID] = c.Title
		catByID[c.ID] = c.Category
	}

	stats := map[string]int{}
	items := make([]reportItem, 0, len(r.Results))
	for _, x := range r.Results {
		stats[x.Status]++
		items = append(items, reportItem{
			Title:    titleByID[x.StepID],
			Status:   x.Status,
			Evidence: x.Evidence,
			Category: catByID[x.StepID],
		})
	}

	missing := make([]reportMissing, 0, len(r.MissingItems))
	for _, m := range r.MissingItems {
		missing = append(missing, reportMissing{
			Title:    m.Title,
			Category: m.Category,
			Severity: m.Severity,
			Reason:   m.Reason,
		})
	}

	total := len(items)
	donePct := 0
	if total > 0 {
		donePct = stats["done"] * 100 / total
	}

	return reportData{
		Task:         cfg.taskTitle,
		Description:  cfg.taskDesc,
		GeneratedAt:  time.Now().Format("2006-01-02 15:04:05"),
		Summary:      r.Summary,
		Stats:        stats,
		TotalSteps:   total,
		DonePercent:  donePct,
		Items:        items,
		MissingItems: missing,
	}
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="th">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Code Audit Report — {{.Task}}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600&family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    background: #f8fafc;
    color: #1e293b;
    padding: 32px 24px;
    line-height: 1.6;
    -webkit-font-smoothing: antialiased;
  }
  code, pre, .mono {
    font-family: 'JetBrains Mono', 'Menlo', monospace;
  }
  .container { max-width: 980px; margin: 0 auto; }
  .header {
    background: #ffffff;
    border: 1px solid #e2e8f0;
    border-radius: 12px;
    padding: 28px;
    margin-bottom: 20px;
    box-shadow: 0 1px 3px rgba(0,0,0,0.04);
  }
  .badge {
    display: inline-block;
    background: #eff6ff;
    color: #2563eb;
    font-size: 11px;
    letter-spacing: 0.8px;
    padding: 4px 10px;
    border-radius: 4px;
    margin-bottom: 12px;
    font-weight: 600;
  }
  h1 {
    color: #0f172a;
    font-size: 26px;
    font-weight: 700;
    margin-bottom: 6px;
    letter-spacing: -0.3px;
  }
  .desc { color: #64748b; font-size: 14px; margin-top: 8px; line-height: 1.6; }
  .meta { color: #94a3b8; font-size: 12px; margin-top: 14px; font-family: 'JetBrains Mono', monospace; }
  .progress-bar {
    height: 6px;
    background: #e2e8f0;
    border-radius: 3px;
    overflow: hidden;
    margin-top: 16px;
  }
  .progress-fill {
    height: 100%;
    background: linear-gradient(90deg, #3b82f6, #8b5cf6);
    transition: width 0.4s;
  }
  .summary-box {
    background: #ffffff;
    border: 1px solid #e2e8f0;
    border-left: 3px solid #3b82f6;
    border-radius: 8px;
    padding: 18px 20px;
    margin-bottom: 20px;
    color: #334155;
    font-size: 14px;
    box-shadow: 0 1px 2px rgba(0,0,0,0.03);
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 12px;
    margin-bottom: 28px;
  }
  .stat {
    padding: 14px 16px;
    background: #ffffff;
    border: 1px solid #e2e8f0;
    border-radius: 8px;
    box-shadow: 0 1px 2px rgba(0,0,0,0.03);
  }
  .stat-label {
    font-size: 10px;
    letter-spacing: 1px;
    color: #94a3b8;
    margin-bottom: 6px;
    font-weight: 600;
  }
  .stat-value { font-size: 24px; font-weight: 700; }
  .stat.done { border-top: 3px solid #10b981; }
  .stat.done .stat-value { color: #059669; }
  .stat.missing { border-top: 3px solid #ef4444; }
  .stat.missing .stat-value { color: #dc2626; }
  .stat.partial { border-top: 3px solid #f59e0b; }
  .stat.partial .stat-value { color: #d97706; }
  .stat.na { border-top: 3px solid #cbd5e1; }
  .stat.na .stat-value { color: #64748b; }
  .section-title {
    font-size: 11px;
    color: #64748b;
    letter-spacing: 1.2px;
    margin: 28px 0 12px 0;
    font-weight: 700;
    text-transform: uppercase;
  }
  .item {
    display: flex;
    gap: 14px;
    padding: 14px 18px;
    background: #ffffff;
    border: 1px solid #e2e8f0;
    border-radius: 8px;
    margin-bottom: 6px;
    transition: box-shadow 0.15s;
  }
  .item:hover { box-shadow: 0 2px 6px rgba(0,0,0,0.06); }
  .item.missing { border-left: 3px solid #ef4444; }
  .item.partial { border-left: 3px solid #f59e0b; }
  .item.done { border-left: 3px solid #10b981; }
  .item.not_applicable { border-left: 3px solid #cbd5e1; opacity: 0.7; }
  .icon {
    font-size: 18px;
    line-height: 1.3;
    min-width: 22px;
    font-weight: 700;
  }
  .icon.done { color: #10b981; }
  .icon.missing { color: #ef4444; }
  .icon.partial { color: #f59e0b; }
  .icon.na { color: #94a3b8; }
  .item-body { flex: 1; }
  .item-title { color: #0f172a; font-size: 14px; font-weight: 500; margin-bottom: 4px; }
  .item-evidence { color: #64748b; font-size: 13px; line-height: 1.55; }
  .cat-badge {
    display: inline-block;
    font-size: 9px;
    padding: 3px 9px;
    border-radius: 4px;
    background: #f1f5f9;
    border: 1px solid #e2e8f0;
    color: #475569;
    font-weight: 700;
    letter-spacing: 0.5px;
    align-self: center;
    text-transform: uppercase;
    font-family: 'JetBrains Mono', monospace;
  }
  .missing-card {
    background: #ffffff;
    border: 1px solid #e2e8f0;
    border-radius: 8px;
    padding: 16px 18px;
    margin-bottom: 8px;
  }
  .missing-card.high { border-left: 3px solid #ef4444; background: #fef2f2; }
  .missing-card.medium { border-left: 3px solid #f59e0b; background: #fffbeb; }
  .missing-card.low { border-left: 3px solid #3b82f6; background: #eff6ff; }
  .missing-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    margin-bottom: 8px;
  }
  .missing-title { color: #0f172a; font-size: 14px; font-weight: 600; }
  .severity {
    font-size: 9px;
    padding: 3px 9px;
    border-radius: 4px;
    font-weight: 700;
    letter-spacing: 0.5px;
    text-transform: uppercase;
    font-family: 'JetBrains Mono', monospace;
  }
  .severity.high { background: #fee2e2; border: 1px solid #fca5a5; color: #b91c1c; }
  .severity.medium { background: #fef3c7; border: 1px solid #fcd34d; color: #b45309; }
  .severity.low { background: #dbeafe; border: 1px solid #93c5fd; color: #1d4ed8; }
  .missing-reason { color: #475569; font-size: 13px; line-height: 1.55; }
  .empty {
    text-align: center;
    padding: 32px;
    background: #f0fdf4;
    border: 1px dashed #86efac;
    border-radius: 8px;
    color: #15803d;
    font-size: 13px;
    font-weight: 500;
  }
  .footer {
    text-align: center;
    color: #94a3b8;
    font-size: 11px;
    margin-top: 40px;
    padding-top: 20px;
    border-top: 1px solid #e2e8f0;
    font-family: 'JetBrains Mono', monospace;
  }
  @media (max-width: 640px) {
    .stats { grid-template-columns: repeat(2, 1fr); }
    body { padding: 16px 12px; }
    h1 { font-size: 22px; }
  }
</style>
</head>
<body>
  <div class="container">
    <div class="header">
      <div class="badge">🔍 CODE AUDIT REPORT</div>
      <h1>{{.Task}}</h1>
      {{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
      <div class="meta">Generated at {{.GeneratedAt}} • {{.TotalSteps}} steps • {{.DonePercent}}% complete</div>
      <div class="progress-bar"><div class="progress-fill" style="width: {{.DonePercent}}%"></div></div>
    </div>

    <div class="summary-box">{{.Summary}}</div>

    <div class="stats">
      <div class="stat done">
        <div class="stat-label">DONE</div>
        <div class="stat-value">{{index .Stats "done"}}</div>
      </div>
      <div class="stat missing">
        <div class="stat-label">MISSING</div>
        <div class="stat-value">{{index .Stats "missing"}}</div>
      </div>
      <div class="stat partial">
        <div class="stat-label">PARTIAL</div>
        <div class="stat-value">{{index .Stats "partial"}}</div>
      </div>
      <div class="stat na">
        <div class="stat-label">N/A</div>
        <div class="stat-value">{{index .Stats "not_applicable"}}</div>
      </div>
    </div>

    <div class="section-title">✓ Checklist Results ({{len .Items}})</div>
    {{range .Items}}
    <div class="item {{.Status}}">
      <div class="icon {{if eq .Status "done"}}done{{else if eq .Status "missing"}}missing{{else if eq .Status "partial"}}partial{{else}}na{{end}}">
        {{if eq .Status "done"}}✓{{else if eq .Status "missing"}}✗{{else if eq .Status "partial"}}◐{{else}}—{{end}}
      </div>
      <div class="item-body">
        <div class="item-title">{{.Title}}</div>
        {{if .Evidence}}<div class="item-evidence">{{.Evidence}}</div>{{end}}
      </div>
      {{if .Category}}<span class="cat-badge">{{.Category}}</span>{{end}}
    </div>
    {{end}}

    <div class="section-title">⚠ Missing Items Not in Checklist ({{len .MissingItems}})</div>
    {{if .MissingItems}}
      {{range .MissingItems}}
      <div class="missing-card {{.Severity}}">
        <div class="missing-header">
          <div class="missing-title">{{.Title}}</div>
          <span class="severity {{.Severity}}">{{.Severity}}</span>
        </div>
        <div class="missing-reason">{{.Reason}}</div>
      </div>
      {{end}}
    {{else}}
      <div class="empty">✓ ไม่มีอะไรขาดเพิ่มเติม</div>
    {{end}}

    <div class="footer">Generated by taskaudit • Powered by Claude</div>
  </div>
</body>
</html>`

func exportHTML(path string, cfg *config, checklist []checklistItem, r *auditResult) error {
	tmpl, err := template.New("report").Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	data := buildReportData(cfg, checklist, r)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}

// ─────────────────────────────────────────────────────────────────
// Export: Markdown
// ─────────────────────────────────────────────────────────────────

func exportMarkdown(path string, cfg *config, checklist []checklistItem, r *auditResult) error {
	data := buildReportData(cfg, checklist, r)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# 🔍 Code Audit Report — %s\n\n", data.Task))
	if data.Description != "" {
		sb.WriteString(fmt.Sprintf("> %s\n\n", data.Description))
	}
	sb.WriteString(fmt.Sprintf("**Generated:** `%s`  \n", data.GeneratedAt))
	sb.WriteString(fmt.Sprintf("**Progress:** `%d/%d steps done (%d%%)`\n\n", data.Stats["done"], data.TotalSteps, data.DonePercent))

	sb.WriteString("---\n\n")
	sb.WriteString("## 📊 Summary\n\n")
	sb.WriteString(data.Summary + "\n\n")

	sb.WriteString("## 📈 Stats\n\n")
	sb.WriteString("| Status | Count |\n")
	sb.WriteString("|--------|-------|\n")
	sb.WriteString(fmt.Sprintf("| ✓ Done | %d |\n", data.Stats["done"]))
	sb.WriteString(fmt.Sprintf("| ✗ Missing | %d |\n", data.Stats["missing"]))
	sb.WriteString(fmt.Sprintf("| ◐ Partial | %d |\n", data.Stats["partial"]))
	sb.WriteString(fmt.Sprintf("| — N/A | %d |\n\n", data.Stats["not_applicable"]))

	sb.WriteString("## ✓ Checklist Results\n\n")
	for _, item := range data.Items {
		var icon string
		switch item.Status {
		case "done":
			icon = "✅"
		case "missing":
			icon = "❌"
		case "partial":
			icon = "🟡"
		default:
			icon = "⚪"
		}
		sb.WriteString(fmt.Sprintf("- %s **%s**", icon, item.Title))
		if item.Category != "" {
			sb.WriteString(fmt.Sprintf(" `%s`", strings.ToUpper(item.Category)))
		}
		sb.WriteString("\n")
		if item.Evidence != "" {
			sb.WriteString(fmt.Sprintf("  - _%s_\n", item.Evidence))
		}
	}
	sb.WriteString("\n")

	sb.WriteString("## ⚠ Missing Items (not in checklist)\n\n")
	if len(data.MissingItems) == 0 {
		sb.WriteString("_✓ ไม่มีอะไรขาดเพิ่มเติม_\n")
	} else {
		for _, m := range data.MissingItems {
			sb.WriteString(fmt.Sprintf("### `%s` — %s\n\n", strings.ToUpper(m.Severity), m.Title))
			sb.WriteString(fmt.Sprintf("%s\n\n", m.Reason))
		}
	}

	sb.WriteString("\n---\n_Generated by taskaudit • Powered by Claude_\n")

	return os.WriteFile(path, []byte(sb.String()), 0644)
}
