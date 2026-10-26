package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jaychou778/degradation-check/lmdetector"
)

const (
	TestCandy       = "candy"
	TestFingerprint = "fingerprint"
	TestPelican     = "pelican"

	StatusCorrect   = "correct"
	StatusWrong     = "wrong"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusCompleted = "completed"
)

// candyPrompt 与原插件 cpa-plugin-codex-candy-eval 的题目一致。
const candyPrompt = `不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）

        苹果味  桃子味  西瓜味
圆形       7      9      8
五角星形   7      6      4
`

const pelicanPreamble = "不使用任何工具，也不要创建或修改文件，直接在回复中完成以下任务：\n\n"

// PelicanVariant 是画鹈鹕的版本。
type PelicanVariant struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
	Format string `json:"format"`
}

// PelicanVariants 两套提示词，与上游插件保持一致。
var PelicanVariants = []PelicanVariant{
	{
		ID:     "original",
		Label:  "原版",
		Format: "html",
		Prompt: pelicanPreamble +
			"创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要使用外部资产。\n\n" +
			"在一个 ```html 代码块中给出完整的 HTML 文件。",
	},
	{
		ID:     "all-in-one",
		Label:  "多合一版",
		Format: "svg",
		Prompt: pelicanPreamble +
			"做一张精细的SVG图片，内容是鹈鹕骑着自行车在孙悟空开的大型飞机机翼上骑行，\n" +
			"鹈鹕展开翅膀，上面托着秦始皇的北极熊，秦始皇骑在熊上打螺丝。鹈鹕喊出糖果题的答案。\n\n" +
			"在一个 ```svg 代码块中给出完整的 SVG 文件。",
	},
}

// FindVariant 按 ID 查版本。
func FindVariant(id string) (PelicanVariant, bool) {
	for _, v := range PelicanVariants {
		if v.ID == id {
			return v, true
		}
	}
	return PelicanVariant{}, false
}

const (
	fingerprintSamples = 3
	fingerprintNumbers = 300
)

// runCandy 跑 N 次糖果题，判定是否出现独立的 21。
func (a *App) runCandy(ctx context.Context, acc Account, model, effort string, runs int) *Result {
	if runs <= 0 {
		runs = 1
	}
	if runs > 10 {
		runs = 10
	}

	r := a.newResult(acc, TestCandy, model, effort, "", nil)
	var last *TestOutcome
	lastText := ""
	for i := 0; i < runs; i++ {
		out, err := a.upstream.TestAccount(ctx, acc.ID, model, candyPrompt, effort)
		if err != nil {
			r.Status, r.Error = StatusFailed, err.Error()
			return r
		}
		last = out
		lastText = out.Text
		if out.Error != "" {
			r.Status, r.Error = StatusFailed, out.Error
			return r
		}
	}

	applyUsage(r, last)
	if strings.TrimSpace(lastText) == "" {
		r.Status = StatusFailed
		if r.Error == "" {
			r.Error = "模型没有返回内容"
		}
		return r
	}
	if hasStandalone21(lastText) {
		r.Status = StatusCorrect
	} else {
		r.Status = StatusWrong
	}
	r.Payload, _ = json.Marshal(map[string]any{
		"answer": truncateRunes(lastText, 4000),
		"runs":   runs,
	})
	return r
}

// runPelican 跑一次画鹈鹕，提取可渲染的文档。
func (a *App) runPelican(ctx context.Context, acc Account, model, effort, variantID string) *Result {
	variant, ok := FindVariant(variantID)
	if !ok {
		variant = PelicanVariants[0]
	}
	r := a.newResult(acc, TestPelican, model, effort, variant.ID, nil)

	out, err := a.upstream.TestAccount(ctx, acc.ID, model, variant.Prompt, effort)
	if err != nil {
		r.Status, r.Error = StatusFailed, err.Error()
		r.Payload, _ = json.Marshal(map[string]any{"html": ""})
		return r
	}
	applyUsage(r, out)
	if out.Error != "" {
		r.Status, r.Error = StatusFailed, out.Error
		r.Payload, _ = json.Marshal(map[string]any{"html": ""})
		return r
	}

	html := extractDocument(out.Text, variant.Format)
	if strings.TrimSpace(html) == "" {
		r.Status = StatusFailed
		r.Error = "回答里没有找到 HTML/SVG；请展开详情检查实际回答，不据此判定降智"
		r.Payload, _ = json.Marshal(map[string]any{"html": "", "raw": truncateRunes(out.Text, 1200)})
		return r
	}
	r.Status = StatusCompleted
	r.Payload, _ = json.Marshal(map[string]any{"html": html, "chars": len(html)})
	return r
}

// runFingerprint 采集三组随机整数序列并做模型归因。
func (a *App) runFingerprint(ctx context.Context, acc Account, model string) *Result {
	r := a.newResult(acc, TestFingerprint, model, "", "", nil)

	samples := make([]map[string]any, 0, fingerprintSamples)
	outputs := make([]lmdetector.Output, 0, fingerprintSamples)
	valid := 0
	var total time.Duration

	for i := 0; i < fingerprintSamples; i++ {
		prompt := fmt.Sprintf("请生成 %d 个 1 到 355 之间的随机整数，用逗号分隔，不要解释，不要编号。", fingerprintNumbers)
		out, err := a.upstream.TestAccount(ctx, acc.ID, model, prompt, "")
		if err != nil {
			samples = append(samples, map[string]any{"error": err.Error()})
			continue
		}
		if out.Error != "" {
			samples = append(samples, map[string]any{"error": out.Error})
			continue
		}
		total += out.Duration
		parsed := countIntegers(out.Text, 1, 355)
		accepted := parsed >= fingerprintNumbers*8/10
		if accepted {
			valid++
			outputs = append(outputs, lmdetector.Output{Text: out.Text, ExpectedCount: fingerprintNumbers})
		}
		samples = append(samples, map[string]any{
			"text":           truncateRunes(out.Text, 6000),
			"parsed_numbers": parsed,
			"expected_count": fingerprintNumbers,
			"accepted":       accepted,
		})
	}
	r.DurationMS = total.Milliseconds()

	payload := map[string]any{
		"samples":           samples,
		"valid_count":       valid,
		"detector_revision": lmdetector.Revision(),
	}
	if valid == 0 {
		r.Status = StatusFailed
		r.Error = "三次挑战都没有拿到有效的数字序列"
		r.Payload, _ = json.Marshal(payload)
		return r
	}

	attr, err := lmdetector.Analyze(outputs)
	if err != nil {
		r.Status = StatusFailed
		r.Error = "归因失败：" + err.Error()
	} else {
		r.Status = StatusCompleted
		payload["attribution"] = attr
	}
	r.Payload, _ = json.Marshal(payload)
	return r
}

// ---------------- 判定与提取 ----------------

// hasStandalone21 与上游语义一致：出现独立的 21 即算答对。
func hasStandalone21(text string) bool {
	for i := 0; i+1 < len(text); i++ {
		if text[i] != '2' || text[i+1] != '1' {
			continue
		}
		beforeOK := i == 0 || !isDigit(text[i-1])
		afterOK := i+2 == len(text) || !isDigit(text[i+2])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

var fencedCode = regexp.MustCompile("(?s)```[ \t]*(?i:html|svg)?[ \t]*\r?\n(.*?)```")

// extractDocument 从回答里取出可渲染的 HTML/SVG，并注入自适应样式。
func extractDocument(text, format string) string {
	for _, m := range fencedCode.FindAllStringSubmatch(text, -1) {
		candidate := strings.TrimSpace(m[1])
		lower := strings.ToLower(candidate)
		if strings.Contains(lower, "<svg") || strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") {
			return injectViewportCSS(wrapDocument(candidate, format))
		}
	}
	lower := strings.ToLower(text)
	if idx := strings.Index(lower, "<svg"); idx >= 0 {
		return injectViewportCSS(wrapDocument(text[idx:], "svg"))
	}
	if idx := strings.Index(lower, "<!doctype html"); idx >= 0 {
		return injectViewportCSS(wrapDocument(text[idx:], "html"))
	}
	if idx := strings.Index(lower, "<html"); idx >= 0 {
		return injectViewportCSS(wrapDocument(text[idx:], "html"))
	}
	return ""
}

func wrapDocument(body, format string) string {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") {
		return body
	}
	if format == "svg" || strings.HasPrefix(strings.TrimSpace(lower), "<svg") {
		return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">` +
			`<style>html,body{margin:0;height:100%;background:#0f172a}svg{display:block;width:100%;height:100%}</style>` +
			`</head><body>` + body + `</body></html>`
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"></head><body>` + body + `</body></html>`
}

// viewportCSS 让没有写 width/height 的 SVG 也能填满容器。
const viewportCSS = `<style id="degradation-viewport-fit">
html, body { margin:0 !important; padding:0 !important; width:100% !important; height:100% !important; overflow:hidden !important; background:#0f172a; }
body { display:flex !important; align-items:center !important; justify-content:center !important; }
body > svg, body > * > svg, #scene { width:100% !important; height:100% !important; max-width:100% !important; max-height:100% !important; }
</style>`

func injectViewportCSS(html string) string {
	lower := strings.ToLower(html)
	if idx := strings.LastIndex(lower, "</head>"); idx >= 0 {
		return html[:idx] + viewportCSS + html[idx:]
	}
	if idx := strings.Index(lower, "<body"); idx >= 0 {
		return html[:idx] + viewportCSS + html[idx:]
	}
	return "<!doctype html><html><head><meta charset=\"utf-8\">" + viewportCSS + "</head><body>" + html + "</body></html>"
}

var integerRe = regexp.MustCompile(`\d+`)

func countIntegers(text string, lo, hi int) int {
	n := 0
	for _, m := range integerRe.FindAllString(text, -1) {
		var v int
		if _, err := fmt.Sscanf(m, "%d", &v); err != nil {
			continue
		}
		if v >= lo && v <= hi {
			n++
		}
	}
	return n
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…（已截断）"
}
