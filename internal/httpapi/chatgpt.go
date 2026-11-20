package httpapi

import (
	"errors"
	"net/http"

	"github.com/devthefuture-org/quotadeck/internal/chatgpt"
	"github.com/devthefuture-org/quotadeck/internal/control"
)

func (s *Server) chatGPTAllowed(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.chatGPT == nil {
		writeError(w, http.StatusServiceUnavailable, "chatgpt_unavailable", "ChatGPT plan access is unavailable")
		return false
	}
	if !isLoopbackRequest(r) || r.Header.Get("X-QuotaDeck-Request") != "control" {
		writeError(w, http.StatusForbidden, "local_only", "ChatGPT controls require a local QuotaDeck request")
		return false
	}
	return true
}

func (s *Server) chatGPTStatus(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTAllowed(w, r) {
		writeJSON(w, http.StatusOK, s.chatGPT.Status())
	}
}

func (s *Server) startChatGPTLogin(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTAllowed(w, r) {
		return
	}
	loginURL, err := s.chatGPT.Start()
	if err != nil {
		writeError(w, http.StatusConflict, "chatgpt_sign_in", "could not start ChatGPT sign-in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": loginURL})
}

func (s *Server) disconnectChatGPT(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTAllowed(w, r) {
		return
	}
	s.chatGPTControl.Lock()
	defer s.chatGPTControl.Unlock()
	if s.controller != nil {
		if err := s.controller.DeactivateChatGPT(); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration_failed", "could not restore the previous Claude Code settings")
			return
		}
		s.engine.Hub().Publish(map[string]any{"type": "control"})
	}
	if err := s.chatGPT.Disconnect(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "chatgpt_disconnect", "could not disconnect ChatGPT")
		return
	}
	writeJSON(w, http.StatusOK, s.chatGPT.Status())
}

func (s *Server) configureChatGPT(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTAllowed(w, r) || !s.controlRequestAllowed(w, r) {
		return
	}
	var input struct {
		Activate        bool    `json:"activate"`
		ClassifierModel *string `json:"classifierModel"`
	}
	if decodeJSON(w, r, &input) != nil || !input.Activate {
		writeError(w, http.StatusBadRequest, "invalid_request", "activate must be true")
		return
	}
	s.chatGPTControl.Lock()
	defer s.chatGPTControl.Unlock()
	models, err := s.chatGPT.Models(r.Context())
	if errors.Is(err, chatgpt.ErrNotConnected) {
		writeError(w, http.StatusConflict, "chatgpt_login_required", "connect ChatGPT and allow QuotaDeck to use your plan first")
		return
	}
	if err != nil || len(models) == 0 {
		writeError(w, http.StatusBadGateway, "chatgpt_models", "could not find a model for your ChatGPT plan")
		return
	}
	if err := s.controller.ConfigureChatGPT(models, input.ClassifierModel); err != nil {
		switch {
		case errors.Is(err, control.ErrChatGPTClassifierUnknown):
			writeError(w, http.StatusBadRequest, "invalid_classifier_model", err.Error())
		case errors.Is(err, control.ErrChatGPTGatewayUnavailable):
			writeError(w, http.StatusServiceUnavailable, "chatgpt_gateway_unavailable", err.Error())
		case errors.Is(err, chatgpt.ErrNotConnected):
			writeError(w, http.StatusConflict, "chatgpt_login_required", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "configuration_failed", "could not configure Claude Code for ChatGPT")
		}
		return
	}
	states, err := s.engine.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_unavailable", "the selection changed but state is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "selected", "control": s.controller.Status(states),
	})
}

func (s *Server) cancelChatGPTLogin(w http.ResponseWriter, r *http.Request) {
	if s.chatGPTAllowed(w, r) {
		s.chatGPT.CancelLogin()
		writeJSON(w, http.StatusOK, s.chatGPT.Status())
	}
}

func (s *Server) chatGPTModels(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTAllowed(w, r) {
		return
	}
	models, err := s.chatGPT.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "chatgpt_models", "could not list models for your ChatGPT plan")
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) chatGPTMessages(w http.ResponseWriter, r *http.Request) {
	if s.chatGPT == nil {
		http.NotFound(w, r)
		return
	}
	s.chatGPT.Messages(w, r)
}
