package teellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const findingAnalysisPromptTemplate = `你是一个代码安全审计专家。请分析以下代码片段是否存在窃取数据、外传数据或其他恶意行为。

## 代码片段
文件: %s (第 %d 行)
` + "```python" + `
%s
>>> %s   ← 触发规则的代码
%s
` + "```" + `

## 触发的规则
规则ID: %s
类别: %s
严重度: %s
规则说明: %s

## 判定优先级与安全边界
1. 恶意/可疑判定 (MALICIOUS 或 SUSPICIOUS)：
   - 必须有上下文明确表明代码在窃取或外发凭据（密钥、令牌、密码、证书、私钥、敏感配置文件）；
   - 或明确在通过未授权网络、隐写载荷、非法持久化通道外传真实的敏感原始数据/特征矩阵；
   - 只有确凿恶意利用证据时才可判定为 MALICIOUS 或 SUSPICIOUS。

2. 良性判定 (BENIGN)（严格消除训练与日志误报）：
   - 日志与标准输出（如 EMB_003）：打印数据集名称/路径标识/类别标签/状态分隔线（例如 print(dataset + '--------')、print(dataset)）、打印训练批次统计、模型参数结构、计算进度、评估指标（如 accuracy、loss、AUC、sensitivity、specificity、F1-score、ci 置信区间等），均属完全正常的训练与科研评估日志，绝非敏感数据泄露，必须判为 BENIGN；
   - 结果保存与导出（如 EMB_001/EMB_002/EMB_004）：正常保存训练生成的模型权重（如 torch.save(model.state_dict(), ...)）、导出评估图表（如 matplotlib/plt 保存 ROC 曲线或分布图）、写入评估结果指标文件，必须判为 BENIGN；
   - 常规配置读取、环境参数封装、合法子进程参数调用等框架良性用法，必须判为 BENIGN。

3. 不确定判定 (UNCERTAIN)：
   - 仅在上下文严重缺失、证据不足且语义完全无法确认意图时返回。

## 请回答
1. verdict: MALICIOUS(恶意) / SUSPICIOUS(可疑) / BENIGN(正常) / UNCERTAIN(不确定)
2. reason: 一句话说明理由（中文）
3. risk: 如果恶意，数据会怎样被利用

请严格按以下 JSON 格式回答，不要包含其他内容:
{"verdict": "...", "reason": "...", "risk": "..."}
`

// OllamaBackendConfig configures the OllamaBackend adapter.
type OllamaBackendConfig struct {
	Endpoint     string
	DefaultModel string
	Timeout      time.Duration
	HTTPClient   *http.Client
}

// OllamaBackend implements teellm.Backend by dispatching prompts to an Ollama server.
type OllamaBackend struct {
	endpoint     string
	defaultModel string
	client       *http.Client
}

var _ Backend = (*OllamaBackend)(nil)

// NewOllamaBackend creates and initializes a new OllamaBackend instance.
func NewOllamaBackend(cfg OllamaBackendConfig) (*OllamaBackend, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}

	model := strings.TrimSpace(cfg.DefaultModel)
	if model == "" {
		model = "qwen2.5-coder:3b"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
		}
	}

	return &OllamaBackend{
		endpoint:     endpoint,
		defaultModel: model,
		client:       client,
	}, nil
}

// HandleHealthCheck queries the Ollama /api/tags endpoint to verify daemon health.
func (b *OllamaBackend) HandleHealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("create ollama health check request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health check returned status %d", resp.StatusCode)
	}

	return nil
}

// ollamaGenerateReq represents the JSON payload expected by Ollama's /api/generate endpoint.
type ollamaGenerateReq struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

// ollamaGenerateResp represents the response body returned by Ollama's /api/generate endpoint.
type ollamaGenerateResp struct {
	Response string `json:"response"`
}

// rawDecision represents the parsed decision JSON fields.
type rawDecision struct {
	Verdict     string `json:"verdict"`
	Reason      string `json:"reason"`
	Risk        string `json:"risk"`
	Remediation string `json:"remediation,omitempty"`
}

// HandleVerifyFinding converts a FindingPayload into a prompt, queries Ollama, and parses the response.
func (b *OllamaBackend) HandleVerifyFinding(ctx context.Context, req *RequestEnvelope) (*ResponseEnvelope, error) {
	if req == nil || req.FindingPayload == nil {
		return &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       reqIDOrEmpty(req),
			Status:          StatusInvalidRequest,
			ErrorMessage:    "missing finding payload",
		}, errors.New("missing finding payload")
	}

	f := req.FindingPayload
	prompt := fmt.Sprintf(findingAnalysisPromptTemplate,
		f.Target.FilePath, f.Target.Line,
		f.Target.ContextBefore, f.Target.CodeSnippet, f.Target.ContextAfter,
		f.RuleID, f.Category, f.Severity, f.Description,
	)

	model := b.defaultModel
	if req.ModelRef != nil && strings.TrimSpace(req.ModelRef.Name) != "" {
		model = strings.TrimSpace(req.ModelRef.Name)
	}

	ollamaReqBody := ollamaGenerateReq{
		Model:  model,
		Prompt: prompt,
		Stream: false,
		Options: map[string]any{
			"temperature": 0.1,
			"num_predict": 160,
		},
	}

	reqBytes, err := json.Marshal(ollamaReqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/api/generate", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("create ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama generate request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return nil, fmt.Errorf("ollama returned HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	var genResp ollamaGenerateResp
	if err := json.NewDecoder(httpResp.Body).Decode(&genResp); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	parsed := parseDecisionJSON(genResp.Response)

	return &ResponseEnvelope{
		ProtocolVersion: CurrentProtocolVersion,
		RequestID:       req.RequestID,
		Status:          StatusSuccess,
		Decision: &DecisionResult{
			Verdict:              parsed.Verdict,
			Explanation:          parsed.Reason,
			RiskLevel:            parsed.Risk,
			SuggestedRemediation: parsed.Remediation,
		},
		EngineInfo: &EngineInfo{
			Backend:     "ollama",
			ModelLoaded: model,
		},
	}, nil
}

func reqIDOrEmpty(req *RequestEnvelope) string {
	if req != nil {
		return req.RequestID
	}
	return ""
}

// parseDecisionJSON extracts and validates a decision JSON block from LLM output text.
func parseDecisionJSON(raw string) rawDecision {
	start := -1
	end := -1
	depth := 0
	for i, ch := range raw {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if start >= 0 && end > start {
			break
		}
	}
	if start < 0 || end <= start {
		return rawDecision{Verdict: VerdictUncertain, Reason: "unable to parse model JSON block"}
	}

	var decision rawDecision
	if err := json.Unmarshal([]byte(raw[start:end]), &decision); err != nil {
		return rawDecision{Verdict: VerdictUncertain, Reason: "malformed JSON in model response: " + err.Error()}
	}

	switch strings.ToUpper(strings.TrimSpace(decision.Verdict)) {
	case VerdictMalicious:
		decision.Verdict = VerdictMalicious
	case VerdictSuspicious:
		decision.Verdict = VerdictSuspicious
	case VerdictBenign:
		decision.Verdict = VerdictBenign
	default:
		decision.Verdict = VerdictUncertain
	}

	return decision
}
