package teellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/CipherSlinger/teetls"
)

// Backend defines the inference handler interface executed by the Server.
type Backend interface {
	Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
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
	mux.HandleFunc("/v1/llm/generate", s.handleGenerate)
	mux.HandleFunc("/v1/llm/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/llm/getAttestation", s.handleGetAttestation)

	s.httpSrv = &http.Server{
		Handler: mux,
	}

	return s, nil
}

// errRequestBodyTooLarge reports that a request body exceeded
// MaxRequestPayloadBytes.
var errRequestBodyTooLarge = errors.New("request body exceeds 1MB limit")

// readLimitedBody reads at most MaxRequestPayloadBytes from the request body.
// Callers own the response shape, so the failure is returned rather than
// written here.
func readLimitedBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(MaxRequestPayloadBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxRequestPayloadBytes {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

// handleGenerate handles POST /v1/llm/generate.
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := readLimitedBody(r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
			err = errRequestBodyTooLarge
		} else {
			err = fmt.Errorf("read request body error: %w", err)
		}
		writeJSON(w, status, &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			Status:          StatusInvalidRequest,
			ErrorMessage:    err.Error(),
		})
		return
	}

	var req GenerateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			Status:          StatusInvalidRequest,
			ErrorMessage:    "invalid request JSON: " + err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusInvalidRequest,
			ErrorMessage:    "prompt must not be empty",
		})
		return
	}

	resp, err := s.backend.Generate(r.Context(), &req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusInternalError,
			ErrorMessage:    err.Error(),
		})
		return
	}
	if resp == nil {
		resp = &GenerateResponse{
			ProtocolVersion: CurrentProtocolVersion,
			RequestID:       req.RequestID,
			Status:          StatusSuccess,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// handleHealthz handles health check queries under GET /v1/llm/healthz.
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
