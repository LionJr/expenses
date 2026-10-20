package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientHTTPContractAndToolDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
			Tools    []Tool    `json:"tools"`
			Parallel bool      `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "test-model" || len(body.Messages) != 1 || body.Messages[0].Content != "кофе 350" || len(body.Tools) == 0 || body.Parallel {
			t.Errorf("incorrect completion payload: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"op-1","type":"function","function":{"name":"create_expense","arguments":"{\"amount_rubles\":\"350\"}"}}]}}]}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL + "/v1/", Key: "test-key", Model: "test-model", HTTP: server.Client()}
	message, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "кофе 350"}}, Tools())
	if err != nil || len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "op-1" || message.ToolCalls[0].Function.Name != "create_expense" || message.ToolCalls[0].Function.Arguments != `{"amount_rubles":"350"}` {
		t.Fatalf("tool call lost during decoding: %+v %v", message, err)
	}
}

func TestClientFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		attempts int
	}{
		{"authentication is not retried", http.StatusUnauthorized, `{}`, 1},
		{"bad request is not retried", http.StatusBadRequest, `{}`, 1},
		{"rate limit retries are bounded", http.StatusTooManyRequests, `{}`, 3},
		{"server error retries are bounded", http.StatusServiceUnavailable, `{}`, 3},
		{"malformed response", http.StatusOK, `not-json`, 1},
		{"missing choices", http.StatusOK, `{"choices":[]}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := Client{BaseURL: server.URL, HTTP: server.Client()}
			_, err := client.Complete(context.Background(), nil, Tools())
			var provider *ProviderError
			if !errors.As(err, &provider) || requests != tc.attempts || provider.Attempts != tc.attempts || provider.Status != tc.status {
				t.Fatalf("incorrect failure policy: requests=%d error=%v", requests, err)
			}
		})
	}
}

func TestListStorageFailureIsRetryable(t *testing.T) {
	repo := &fakeRepo{listError: errors.New("database temporarily unavailable")}
	llm := &script{responses: []Message{call("list_expenses", `{}`)}}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "покажи траты за неделю", Session{})
	var processing *ProcessingError
	if !errors.As(err, &processing) || reply != "" || repo.listCalls != 1 || repo.mutationAttempts != 0 {
		t.Fatalf("storage outage treated as an empty successful list: %q %v", reply, err)
	}
}
