package backend

// OpenAI-compatible chat-completions backend: the lingua-franca dialect
// spoken by OpenRouter, local inference servers (llama.cpp, Ollama, vLLM),
// and most providers' compatibility endpoints. Implemented directly over
// net/http — no vendor SDK.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAI struct {
	BaseURL string // e.g. "https://openrouter.ai/api/v1"
	APIKey  string
	HTTP    *http.Client
}

func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 300 * time.Second},
	}
}

// Wire shapes for the chat-completions dialect.

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	Tools     []oaTool    `json:"tools,omitempty"`
	MaxTokens int         `json:"max_tokens,omitempty"`
}

type oaResponse struct {
	// Model is the concrete model the endpoint used to serve this response.
	// Router endpoints report the model actually chosen, not the alias asked
	// for.
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Complete(ctx context.Context, req Request) (*Turn, error) {
	oreq := oaRequest{Model: req.Model, MaxTokens: req.MaxTokens}

	if req.System != "" {
		oreq.Messages = append(oreq.Messages, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			otc := oaToolCall{ID: tc.ID, Type: "function"}
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Arguments
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		oreq.Messages = append(oreq.Messages, om)
	}
	for _, t := range req.Tools {
		ot := oaTool{Type: "function"}
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		oreq.Tools = append(oreq.Tools, ot)
	}

	body, err := json.Marshal(oreq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	hresp, err := o.HTTP.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("backend request: %w", err)
	}
	defer hresp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(hresp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read backend response: %w", err)
	}

	var oresp oaResponse
	if err := json.Unmarshal(respBody, &oresp); err != nil {
		return nil, fmt.Errorf("backend returned HTTP %d with unparseable body: %s",
			hresp.StatusCode, snippet(respBody))
	}
	if hresp.StatusCode != http.StatusOK {
		msg := snippet(respBody)
		if oresp.Error != nil && oresp.Error.Message != "" {
			msg = oresp.Error.Message
		}
		return nil, fmt.Errorf("backend HTTP %d: %s", hresp.StatusCode, msg)
	}
	if oresp.Error != nil && oresp.Error.Message != "" {
		// Some gateways (OpenRouter among them) report upstream failures
		// inside a 200 body.
		return nil, fmt.Errorf("backend error: %s", oresp.Error.Message)
	}
	if len(oresp.Choices) == 0 {
		return nil, fmt.Errorf("backend returned no choices: %s", snippet(respBody))
	}

	choice := oresp.Choices[0]
	turn := &Turn{Content: choice.Message.Content, Model: oresp.Model}
	for _, otc := range choice.Message.ToolCalls {
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			ID:        otc.ID,
			Name:      otc.Function.Name,
			Arguments: otc.Function.Arguments,
		})
	}

	switch {
	case len(turn.ToolCalls) > 0:
		// Trust the shape over the label: some models emit tool calls
		// with finish_reason "stop".
		turn.StopReason = StopToolUse
	case choice.FinishReason == "length":
		turn.StopReason = StopMaxTokens
	default:
		turn.StopReason = StopEndTurn
	}
	return turn, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	if s == "" {
		s = "(empty body)"
	}
	return s
}
