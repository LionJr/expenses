package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type Tool struct {
	Type     string     `json:"type"`
	Function Definition `json:"function"`
}
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
type Completer interface {
	Complete(context.Context, []Message, []Tool) (Message, error)
}
type Client struct {
	BaseURL, Key, Model string
	HTTP                *http.Client
}

type completionResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	data, err := json.Marshal(map[string]any{"model": c.Model, "messages": messages, "tools": tools, "tool_choice": "auto", "parallel_tool_calls": false, "temperature": 0, "max_completion_tokens": 1000})
	if err != nil {
		return Message{}, providerFailure("request_encoding", 0, 0, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(data))
		if e != nil {
			return Message{}, providerFailure("configuration", 0, attempt+1, e)
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Content-Type", "application/json")

		resp, e := c.HTTP.Do(req)
		if e != nil {
			return Message{}, providerFailure("network", 0, attempt+1, e)
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()

		if e != nil {
			return Message{}, providerFailure("response_read", resp.StatusCode, attempt+1, e)
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError) && attempt < 2 {
			delay := time.Duration(attempt+1) * time.Second
			if v := resp.Header.Get("Retry-After"); v != "" {
				if n, e := strconv.Atoi(v); e == nil {
					delay = time.Duration(n) * time.Second
				} else if t, e := http.ParseTime(v); e == nil {
					delay = time.Until(t)
				}
			}
			if delay < 0 {
				delay = 0
			}
			if delay > 10*time.Second {
				return Message{}, providerFailure(statusKind(resp.StatusCode), resp.StatusCode, attempt+1, nil)
			}
			slog.Info("LLM request retry", "operation", "complete", "status", resp.StatusCode, "retry_count", attempt+1, "delay_ms", delay.Milliseconds())
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Message{}, providerFailure("canceled", resp.StatusCode, attempt+1, ctx.Err())
			case <-timer.C:
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return Message{}, providerFailure(statusKind(resp.StatusCode), resp.StatusCode, attempt+1, nil)
		}
		var result completionResponse
		if err = json.Unmarshal(body, &result); err != nil || len(result.Choices) != 1 {
			return Message{}, providerFailure("invalid_response", resp.StatusCode, attempt+1, err)
		}
		m := result.Choices[0].Message
		m.Role = "assistant"
		if len(m.ToolCalls) > 5 || len(m.Content) > 12000 {
			return Message{}, providerFailure("response_too_large", resp.StatusCode, attempt+1, nil)
		}
		return m, nil
	}
	return Message{}, providerFailure("provider_unavailable", 0, 3, nil)
}
