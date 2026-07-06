package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpschema "github.com/gluonfield/acp-transport/acp"
	"github.com/gluonfield/acp-transport/jsonrpc"
	agy "github.com/gluonfield/agy-go"
)

const (
	modeFullAccess = "full-access"
	modePlan       = "plan"
)

type Options struct {
	Backend                    string
	DefaultModel               string
	DefaultTimeout             time.Duration
	DangerouslySkipPermissions bool
}

type Server struct {
	agent agy.Agent
	opts  Options
	peer  *jsonrpc.Peer

	mu       sync.Mutex
	sessions map[string]*session
	cancel   map[string]context.CancelFunc
	seq      atomic.Uint64
}

type session struct {
	ID             string
	Cwd            string
	ConversationID string
	SystemPrompt   string
	SystemSent     bool
	Mode           string
	Model          string
}

func New(agent agy.Agent, opts Options) *Server {
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 5 * time.Minute
	}
	return &Server{
		agent:    agent,
		opts:     opts,
		sessions: map[string]*session{},
		cancel:   map[string]context.CancelFunc{},
	}
}

func (s *Server) SetPeer(peer *jsonrpc.Peer) {
	s.peer = peer
}

func (s *Server) HandleJSONRPC(ctx context.Context, req jsonrpc.Request) (json.RawMessage, *jsonrpc.Error) {
	switch req.Method {
	case acpschema.AgentMethodInitialize:
		return jsonrpc.EncodeResult(s.initialize())
	case acpschema.AgentMethodSessionNew:
		return s.sessionNew(req.Params)
	case acpschema.AgentMethodSessionLoad, acpschema.AgentMethodSessionResume:
		return s.sessionLoad(req.Params)
	case acpschema.AgentMethodSessionPrompt:
		return s.sessionPrompt(ctx, req.Params)
	case acpschema.AgentMethodSessionCancel:
		s.sessionCancel(req.Params)
		return nil, nil
	case acpschema.AgentMethodSessionClose:
		return s.sessionClose(req.Params)
	case acpschema.AgentMethodSessionSetMode:
		return s.sessionSetMode(req.Params)
	case acpschema.AgentMethodSessionSetConfigOption:
		return s.sessionSetConfigOption(req.Params)
	default:
		return nil, jsonrpc.MethodNotFound(req.Method)
	}
}

func (s *Server) initialize() acpschema.InitializeResponse {
	return acpschema.InitializeResponse{
		ProtocolVersion: acpschema.ProtocolVersion(acpschema.ProtocolVersionNumber),
		AgentInfo: &acpschema.Implementation{
			Name:    "agy-acp",
			Title:   "Antigravity",
			Version: "0.1.3",
		},
		AgentCapabilities: &acpschema.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: &acpschema.PromptCapabilities{
				EmbeddedContext: true,
			},
			SessionCapabilities: &acpschema.SessionCapabilities{
				Close:  &acpschema.SessionCloseCapabilities{},
				Resume: &acpschema.SessionResumeCapabilities{},
			},
			Meta: map[string]any{"agyAuth": s.opts.Backend},
		},
	}
}

func (s *Server) sessionNew(params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/new params", map[string]any{"error": err.Error()})
	}
	id := newSessionID()
	state := &session{
		ID:           id,
		Cwd:          req.Cwd,
		SystemPrompt: systemPrompt(req.Meta),
		Mode:         modeFullAccess,
		Model:        s.opts.DefaultModel,
	}
	s.mu.Lock()
	s.sessions[id] = state
	s.mu.Unlock()
	return jsonrpc.EncodeResult(map[string]any{
		"sessionId":     id,
		"modes":         s.modeState(state.Mode),
		"configOptions": s.configOptions(state.Model),
	})
}

func (s *Server) sessionLoad(params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.LoadSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/load params", map[string]any{"error": err.Error()})
	}
	id := string(req.SessionID)
	state := &session{
		ID:           id,
		Cwd:          req.Cwd,
		SystemPrompt: systemPrompt(req.Meta),
		Mode:         modeFullAccess,
		Model:        s.opts.DefaultModel,
	}
	s.mu.Lock()
	s.sessions[id] = state
	s.mu.Unlock()
	return jsonrpc.EncodeResult(map[string]any{
		"modes":         s.modeState(state.Mode),
		"configOptions": s.configOptions(state.Model),
	})
}

func (s *Server) sessionPrompt(ctx context.Context, params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.PromptRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/prompt params", map[string]any{"error": err.Error()})
	}
	id := string(req.SessionID)
	text, err := promptText(req.Prompt)
	if err != nil {
		return nil, jsonrpc.InvalidParams("invalid prompt content", map[string]any{"error": err.Error()})
	}
	state, ok := s.snapshot(id)
	if !ok {
		return nil, jsonrpc.InvalidParams("unknown session", map[string]any{"sessionId": id})
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel[id] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancel, id)
		s.mu.Unlock()
	}()

	systemInstructions := ""
	if !state.SystemSent {
		systemInstructions = state.SystemPrompt
	}
	resp, err := s.agent.Chat(runCtx, agy.ChatRequest{
		SessionID:                  state.ID,
		Cwd:                        state.Cwd,
		ConversationID:             state.ConversationID,
		Message:                    text,
		Model:                      state.Model,
		SystemInstructions:         systemInstructions,
		Plan:                       state.Mode == modePlan,
		DangerouslySkipPermissions: s.opts.DangerouslySkipPermissions,
		Timeout:                    s.opts.DefaultTimeout,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return jsonrpc.EncodeResult(acpschema.PromptResponse{StopReason: acpschema.StopReasonCancelled})
		}
		return nil, jsonrpc.InternalError("antigravity turn failed", map[string]any{"error": err.Error()})
	}
	s.updateSessionAfterPrompt(id, resp.ConversationID, systemInstructions != "")
	if resp.PlanText != "" {
		_ = s.notifyPlan(context.Background(), id, resp.PlanText)
	}
	if strings.TrimSpace(resp.Text) != "" {
		_ = s.notifyText(context.Background(), id, resp.Text)
	}
	return jsonrpc.EncodeResult(acpschema.PromptResponse{StopReason: acpschema.StopReasonEndTurn})
}

func (s *Server) sessionCancel(params json.RawMessage) {
	var req acpschema.CancelNotification
	_ = json.Unmarshal(params, &req)
	s.mu.Lock()
	cancel := s.cancel[string(req.SessionID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) sessionClose(params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.CloseSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/close params", map[string]any{"error": err.Error()})
	}
	id := string(req.SessionID)
	s.mu.Lock()
	if cancel := s.cancel[id]; cancel != nil {
		cancel()
	}
	delete(s.cancel, id)
	delete(s.sessions, id)
	s.mu.Unlock()
	return jsonrpc.EncodeResult(acpschema.CloseSessionResponse{})
}

func (s *Server) sessionSetMode(params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.SetSessionModeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/set_mode params", map[string]any{"error": err.Error()})
	}
	mode := string(req.ModeID)
	if mode != modeFullAccess && mode != modePlan {
		return nil, jsonrpc.InvalidParams("unknown mode", map[string]any{"modeId": mode})
	}
	id := string(req.SessionID)
	s.mu.Lock()
	state := s.sessions[id]
	if state != nil {
		state.Mode = mode
	}
	s.mu.Unlock()
	if state == nil {
		return nil, jsonrpc.InvalidParams("unknown session", map[string]any{"sessionId": id})
	}
	_ = s.notifyCurrentMode(context.Background(), id, mode)
	return jsonrpc.EncodeResult(acpschema.SetSessionModeResponse{})
}

func (s *Server) sessionSetConfigOption(params json.RawMessage) (json.RawMessage, *jsonrpc.Error) {
	var req acpschema.SetSessionConfigOptionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, jsonrpc.InvalidParams("invalid session/set_config_option params", map[string]any{"error": err.Error()})
	}
	id := string(req.SessionID)
	s.mu.Lock()
	state := s.sessions[id]
	if state != nil {
		switch string(req.ConfigID) {
		case "model":
			state.Model = string(req.Value)
		}
	}
	model := ""
	if state != nil {
		model = state.Model
	}
	s.mu.Unlock()
	if state == nil {
		return nil, jsonrpc.InvalidParams("unknown session", map[string]any{"sessionId": id})
	}
	return jsonrpc.EncodeResult(map[string]any{"configOptions": s.configOptions(model)})
}

func (s *Server) snapshot(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return session{}, false
	}
	return *state, true
}

func (s *Server) updateSessionAfterPrompt(id, conversationID string, systemSent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return
	}
	if conversationID != "" {
		state.ConversationID = conversationID
	}
	if systemSent {
		state.SystemSent = true
	}
}

func (s *Server) modeState(current string) map[string]any {
	return map[string]any{
		"currentModeId": current,
		"availableModes": []map[string]string{
			{"id": modeFullAccess, "name": "Full Access", "description": "Use Antigravity normally."},
			{"id": modePlan, "name": "Plan", "description": "Ask Antigravity to create an implementation plan first."},
		},
	}
}

func (s *Server) configOptions(currentModel string) []map[string]any {
	models := s.modelOptions(currentModel)
	return []map[string]any{
		{
			"id":           "model",
			"name":         "Model",
			"category":     "model",
			"currentValue": firstNonEmpty(currentModel, firstOption(models)),
			"options":      models,
		},
	}
}

func (s *Server) modelOptions(current string) []map[string]string {
	models, err := s.agent.ListModels(context.Background())
	if err != nil || len(models) == 0 {
		models = []agy.Model{{Name: firstNonEmpty(current, "gemini-3.5-flash")}}
	}
	options := make([]map[string]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		options = append(options, map[string]string{"value": name, "name": name})
	}
	return options
}

func (s *Server) notifyText(ctx context.Context, sessionID, text string) error {
	return s.notify(ctx, sessionID, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     s.messageID("message"),
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func (s *Server) notifyPlan(ctx context.Context, sessionID, text string) error {
	entries := agy.PlanEntries(text)
	if len(entries) == 0 {
		entries = []string{text}
	}
	out := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		out = append(out, map[string]string{
			"content":  entry,
			"status":   "pending",
			"priority": "medium",
		})
	}
	if len(out) == 0 {
		return nil
	}
	return s.notify(ctx, sessionID, map[string]any{
		"sessionUpdate": "plan",
		"entries":       out,
	})
}

func (s *Server) notifyCurrentMode(ctx context.Context, sessionID, mode string) error {
	return s.notify(ctx, sessionID, map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": mode,
	})
}

func (s *Server) notify(ctx context.Context, sessionID string, update map[string]any) error {
	if s.peer == nil {
		return nil
	}
	return s.peer.Notify(ctx, acpschema.ClientMethodSessionUpdate, map[string]any{
		"sessionId": sessionID,
		"update":    update,
	})
}

func (s *Server) messageID(prefix string) string {
	return fmt.Sprintf("agy:%s:%d", prefix, s.seq.Add(1))
}

func promptText(blocks []acpschema.ContentBlock) (string, error) {
	var parts []string
	for _, block := range blocks {
		var text struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &text); err != nil {
			return "", err
		}
		if text.Type == "text" {
			parts = append(parts, text.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

func systemPrompt(meta map[string]any) string {
	value := meta["systemPrompt"]
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if appendText, ok := typed["append"].(string); ok {
			return appendText
		}
	}
	return ""
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("agy-%d", time.Now().UnixNano())
	}
	return "agy-" + hex.EncodeToString(b[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstOption(options []map[string]string) string {
	if len(options) == 0 {
		return ""
	}
	return options[0]["value"]
}
