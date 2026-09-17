package teellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"taa/teetls"
)

// Backend defines the inference handler interface to be executed by the Server.
type Backend interface {
	HandleVerifyFinding(ctx context.Context, req *RequestEnvelope) (*ResponseEnvelope, error)
	HandleHealthCheck(ctx context.Context) error
}

// ServerConfig specifies configuration options for initializing the TEE-LLM Server.
type ServerConfig struct {
	Addr    string
	TEETLS  *teetls.Config
	Backend Backend
}

// Server provides the HTTP REST server for teellm-protocol/v1.
type Server struct {
	cfg      ServerConfig
	listener net.Listener
	httpSrv  *http.Server
	backend  Backend
}

// NewServer initializes a new Server listening on the configured address.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Backend == nil {
		return nil, errors.New("backend must not be nil")
	}

	var listener net.Listener
	var err error

	if cfg.TEETLS != nil {
		listener, err = teetls.Listen("tcp", cfg.Addr, cfg.TEETLS)
	} else {
		listener, err = net.Listen("tcp", cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("server listen error: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		listener: listener,
		backend:  cfg.Backend,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/verify", s.handleVerify)
	mux.HandleFunc("/healthz", s.handleHealthz)

	s.httpSrv = &http.Server{
		Handler: mux,
	}

	return s, nil
}

// handleVerify handles finding verification requests under POST /v1/verify.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limited := io.LimitReader(r.Body, int64(MaxResponsePayloadBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		http.Error(w, "read request body error", http.StatusBadRequest)
		return
	}
	if len(body) > MaxResponsePayloadBytes {
		resp := &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			Status:          StatusInvalidRequest,
			ErrorMessage:    "request body exceeds 1MB limit",
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	var req RequestEnvelope
	if err := json.Unmarshal(body, &req); err != nil {
		resp := &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			Status:          StatusInvalidRequest,
			ErrorMessage:    "invalid request envelope JSON: " + err.Error(),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp, err := s.backend.HandleVerifyFinding(r.Context(), &req)
	if err != nil {
		errResp := &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusInternalError,
			ErrorMessage:    err.Error(),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errResp)
		return
	}

	if resp == nil {
		resp = &ResponseEnvelope{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusSuccess,
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleHealthz handles health check queries under GET /healthz.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.backend.HandleHealthCheck(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// Start runs the HTTP server. It returns nil when the server is cleanly closed.
func (s *Server) Start() error {
	err := s.httpSrv.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close gracefully closes the HTTP server and listener.
func (s *Server) Close() error {
	return s.httpSrv.Close()
}

// Addr returns the network address the server is listening on.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.Addr
}
