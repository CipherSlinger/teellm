package teellm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	cx509 "crypto/x509"

	"github.com/CipherSlinger/teetls"
	"github.com/CipherSlinger/teetls/pkg/csvattest"
)

// AttestationValues carries the report fields the platform stores verbatim.
// The JSON tags match taa/internal/attestation.ReportValues exactly, so the
// platform's existing display and verification code needs no change.
type AttestationValues struct {
	UserData string `json:"userdata"`
	MNonce   string `json:"mnonce"`
	Digest   string `json:"digest"`
	ChipID   string `json:"chipId"`
}

// AttestationSnapshot is one consistent view of what this instance presents:
// the certificate it authenticates with, the CSV report embedded in that
// certificate's evidence extension, and whether that report self-verifies.
//
// Every field is taken from the same cached certificate. Re-fetching a report
// from the device would open a window where the report and the certificate
// disagree, and "report belongs to this certificate" is precisely what the
// platform is checking.
type AttestationSnapshot struct {
	CertificatePEM []byte
	PublicKeyPEM   string
	Report         []byte
	Values         AttestationValues
	VerifiedPass   bool
	// VerifyMessage carries the reason a failed self-check failed, so the
	// response can say "自检验证失败: <原因>" the way TAA does.
	VerifyMessage string
}

// ValuesJSON renders Values the way the platform expects to receive it: a JSON
// object carried inside a JSON string field.
func (s *AttestationSnapshot) ValuesJSON() string {
	data, err := json.Marshal(s.Values)
	if err != nil {
		return ""
	}
	return string(data)
}

// BuildAttestationSnapshot reads the certificate currently cached by cfg and
// decodes the CSV evidence embedded in it. It never triggers a second hardware
// IOCTL: the certificate cache is the single source for both this call and the
// TEE-TLS handshake.
func BuildAttestationSnapshot(ctx context.Context, cfg *teetls.Config) (*AttestationSnapshot, error) {
	if cfg == nil {
		return nil, errors.New("attestation snapshot requires a TEE-TLS config")
	}

	certPEM, _, err := cfg.GetOrGenerateCertificateContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("obtain current certificate: %w", err)
	}
	cert, err := teetls.ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse current certificate: %w", err)
	}

	var evidenceDER []byte
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(teetls.OIDCSVEvidence) {
			evidenceDER = ext.Value
			break
		}
	}
	if evidenceDER == nil {
		return nil, teetls.ErrMissingCSVEvidence
	}
	evidence, err := teetls.DecodeCSVEvidence(evidenceDER)
	if err != nil {
		return nil, fmt.Errorf("decode csv evidence: %w", err)
	}

	res, err := csvattest.ParseReport(evidence.Report)
	if err != nil {
		return nil, fmt.Errorf("parse attestation report: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: cert.RawSubjectPublicKeyInfo})
	verified, verifyMsg := snapshotVerifies(evidence, cfg, cert)

	return &AttestationSnapshot{
		CertificatePEM: append([]byte(nil), certPEM...),
		PublicKeyPEM:   string(pubPEM),
		Report:         append([]byte(nil), evidence.Report...),
		Values: AttestationValues{
			UserData: hex.EncodeToString(res.UserData),
			MNonce:   hex.EncodeToString(res.MNonce),
			Digest:   hex.EncodeToString(res.Digest),
			ChipID:   strings.TrimRight(string(res.ChipID), "\x00"),
		},
		VerifiedPass:  verified,
		VerifyMessage: verifyMsg,
	}, nil
}

// snapshotVerifies runs the same chain check the platform will run, plus the
// public-key binding check. A failure here does not withhold the report; it
// only marks it unverified, matching how TAA degrades on a failed self-check.
func snapshotVerifies(evidence *teetls.CSVEvidenceExtension, cfg *teetls.Config, cert *cx509.Certificate) (bool, string) {
	opts := csvattest.VerifyOptions{VerifyChain: true}
	switch {
	case cfg.HRKCertPath != "" && cfg.HSKCekCertPath != "":
		opts.TrustedHRKCertPath = cfg.HRKCertPath
		opts.HSKCekCertPath = cfg.HSKCekCertPath
	case len(cfg.TrustedHRKCert) > 0 && len(evidence.HSKCekCert) > 0:
		opts.TrustedHRKCertBytes = cfg.TrustedHRKCert
		opts.HSKCekCertBytes = evidence.HSKCekCert
	case len(evidence.HRKCert) > 0 && len(evidence.HSKCekCert) > 0:
		opts.TrustedHRKCertBytes = evidence.HRKCert
		opts.HSKCekCertBytes = evidence.HSKCekCert
	default:
		return false, "没有可信 HRK/HSK-CEK 证书可用于链验"
	}

	res, err := csvattest.VerifyReportWithOptions(evidence.Report, opts)
	if err != nil {
		return false, err.Error()
	}
	bound := teetls.ComputePublicKeySM3(cert.RawSubjectPublicKeyInfo)
	if len(res.UserData) < 32 || !bytes.Equal(res.UserData[:32], bound[:]) {
		return false, "报告 userdata 与证书公钥的 SM3 不一致"
	}
	return true, ""
}

// apiResponse is the envelope TAA already answers with, reproduced here so the
// platform parses both with one decoder.
type apiResponse struct {
	Msg    string `json:"msg"`
	Result any    `json:"result"`
	Error  int    `json:"error"`
}

type getAttestationRequest struct {
	RequestID string `json:"requestId"`
}

func writeAPIResponse(w http.ResponseWriter, status int, msg string, result any, errorCode int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResponse{Msg: msg, Result: result, Error: errorCode})
}

// handleGetAttestation answers the platform on the TEE-TLS listener. The
// response carries the certificate as well as the report so the caller can
// check SM3(certificate public key) == userdata[:32] against the bytes it was
// given, rather than against whichever certificate the handshake happened to
// serve if the cache rotated in between.
func (s *Server) handleGetAttestation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, int64(MaxResponsePayloadBytes)+1))
	if err != nil {
		writeAPIResponse(w, http.StatusBadRequest, "读取请求体失败", nil, http.StatusBadRequest)
		return
	}
	if len(body) > MaxResponsePayloadBytes {
		writeAPIResponse(w, http.StatusRequestEntityTooLarge, "请求体超过 1MB 限制", nil, http.StatusRequestEntityTooLarge)
		return
	}

	var req getAttestationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPIResponse(w, http.StatusBadRequest, "请求解析失败: "+err.Error(), nil, http.StatusBadRequest)
		return
	}
	if req.RequestID == "" {
		writeAPIResponse(w, http.StatusBadRequest, "requestId 不能为空", nil, http.StatusBadRequest)
		return
	}

	snapshot, err := BuildAttestationSnapshot(r.Context(), s.cfg.TEETLS)
	if err != nil {
		writeAPIResponse(w, http.StatusInternalServerError, "获取远程证明失败: "+err.Error(), nil, http.StatusInternalServerError)
		return
	}

	msg := "success"
	if !snapshot.VerifiedPass {
		msg = "自检验证失败: " + snapshot.VerifyMessage
	}

	writeAPIResponse(w, http.StatusOK, msg, map[string]any{
		"requestId":         req.RequestID,
		"verifiedPass":      snapshot.VerifiedPass,
		"attestation":       base64.StdEncoding.EncodeToString(snapshot.Report),
		"attestationValues": snapshot.ValuesJSON(),
		"certificate":       string(snapshot.CertificatePEM),
	}, 0)
}
