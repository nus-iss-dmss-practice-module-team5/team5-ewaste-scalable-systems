package matching

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthConfig configures matching workload-token validation.
// Workload tokens use a dedicated signing key, not user access/refresh secrets.
// Issuer and audience must be explicitly selected by the deployment operator.
type AuthConfig struct {
	Issuer, Audience, Secret string
	OperatorSecret           string
	MaxBodyBytes             int64
}
type workloadClaims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

func RegisterFromEnv(r *gin.Engine, db *gorm.DB) error {
	if os.Getenv("EWASTE_MATCHING_ENABLED") != "true" {
		return nil
	}
	cfg := AuthConfig{Issuer: os.Getenv("EWASTE_MATCHING_ISSUER"), Audience: os.Getenv("EWASTE_MATCHING_AUDIENCE"), Secret: os.Getenv("EWASTE_MATCHING_SIGNING_SECRET"), MaxBodyBytes: 16 << 20}
	cfg.OperatorSecret = os.Getenv("EWASTE_MATCHING_OPERATOR_SIGNING_SECRET")
	if file := os.Getenv("EWASTE_MATCHING_SIGNING_SECRET_FILE"); file != "" {
		value, err := os.ReadFile(file)
		if err != nil {
			return errors.New("read matching signing secret")
		}
		cfg.Secret = strings.TrimSpace(string(value))
	}
	if value := os.Getenv("EWASTE_MATCHING_MAX_BODY_BYTES"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1024 || n > 64<<20 {
			return errors.New("invalid matching body limit")
		}
		cfg.MaxBodyBytes = n
	}
	return Register(r, NewStore(db), cfg)
}

func Register(r *gin.Engine, store *Store, cfg AuthConfig) error {
	if cfg.Issuer == "" || cfg.Audience == "" || len(cfg.Secret) < 32 || cfg.MaxBodyBytes < 1024 {
		return errors.New("matching workload issuer, audience, dedicated signing key and body limit are required")
	}
	if cfg.OperatorSecret != "" && (len(cfg.OperatorSecret) < 32 || cfg.OperatorSecret == cfg.Secret) {
		return errors.New("matching operator key must be distinct and at least 32 bytes")
	}
	errorResponse := func(c *gin.Context, err error) {
		code := "UNAVAILABLE"
		if contract, ok := errors.AsType[contractError](err); ok {
			code = string(contract)
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			code = "NOT_FOUND"
		}
		status := map[string]int{"INVALID_CONTRACT": 400, "LIMIT_EXCEEDED": 413, "UNAUTHENTICATED": 401, "FORBIDDEN": 403, "NOT_FOUND": 404, "IDEMPOTENCY_CONFLICT": 409, "STALE_CONTEXT": 409, "STATE_CONFLICT": 409, "INVALID_RESULT": 422, "UNSUPPORTED_RULE_SET": 503, "UNAVAILABLE": 503}[code]
		if status == 0 {
			status = 503
			code = "UNAVAILABLE"
		}
		trace := c.Writer.Header().Get("X-Correlation-ID")
		if trace == "" || len(trace) > 100 {
			trace = uuid.NewString()
		}
		if status == 503 {
			c.Header("Retry-After", "2")
		}
		slog.Warn("matching_request_failed", "code", code, "transport_correlation_id", trace, "run_id", c.Param("run_id"))
		c.AbortWithStatusJSON(status, object{"code": code, "message": code, "retryable": status == 503, "transport_correlation_id": trace})
	}
	authorize := func(scope string) gin.HandlerFunc {
		return func(c *gin.Context) {
			h := c.GetHeader("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				errorResponse(c, fail("UNAUTHENTICATED"))
				return
			}
			claims := &workloadClaims{}
			operator := false
			token, err := jwt.ParseWithClaims(strings.TrimPrefix(h, "Bearer "), claims, func(t *jwt.Token) (any, error) {
				if t.Header["kid"] == "operator" {
					if cfg.OperatorSecret == "" {
						return nil, errors.New("operator authority not configured")
					}
					operator = true
					return []byte(cfg.OperatorSecret), nil
				}
				if kid, exists := t.Header["kid"]; exists && kid != "worker" {
					return nil, errors.New("unknown signing key")
				}
				return []byte(cfg.Secret), nil
			}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(cfg.Issuer), jwt.WithAudience(cfg.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(5*time.Second))
			subject := "matching-worker"
			if operator {
				subject = "matching-operator"
			}
			if err != nil || !token.Valid || claims.Subject != subject {
				errorResponse(c, fail("UNAUTHENTICATED"))
				return
			}
			permissions := map[string]bool{}
			for _, s := range strings.Fields(claims.Scope) {
				permissions[s] = true
			}
			permissions["matching.rerun"] = operator && permissions["matching.rerun"]
			if !permissions[scope] {
				errorResponse(c, fail("FORBIDDEN"))
				return
			}
			c.Set("matching.permissions", permissions)
			c.Next()
		}
	}
	readBody := func(c *gin.Context) (object, bool) {
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxBodyBytes))
		if err != nil {
			errorResponse(c, fail("LIMIT_EXCEEDED"))
			return nil, false
		}
		body, err := decode(raw)
		if err != nil {
			errorResponse(c, err)
			return nil, false
		}
		return body, true
	}
	pathID := func(c *gin.Context) bool {
		if !validUUID(c.Param("run_id")) {
			errorResponse(c, fail("INVALID_CONTRACT"))
			return false
		}
		return true
	}
	base := "/internal/v1/matching/runs"
	r.POST(base, authorize("matching.execute"), func(c *gin.Context) {
		body, ok := readBody(c)
		if !ok {
			return
		}
		permissions, _ := c.Get("matching.permissions")
		if body["trigger_type"] == "EXPLICIT_RUN" && !permissions.(map[string]bool)["matching.rerun"] {
			errorResponse(c, fail("FORBIDDEN"))
			return
		}
		response, created, err := store.Prepare(c.Request.Context(), body, c.GetHeader("Idempotency-Key"))
		if err != nil {
			errorResponse(c, err)
			return
		}
		status := 200
		if created && response["phase"] == "PREPARED" {
			status = 201
		}
		c.JSON(status, response)
	})
	r.GET(base+"/:run_id", authorize("matching.read"), func(c *gin.Context) {
		if !pathID(c) {
			return
		}
		response, err := store.Get(c.Request.Context(), c.Param("run_id"))
		if err != nil {
			errorResponse(c, err)
			return
		}
		c.JSON(200, response)
	})
	r.POST(base+"/:run_id/refresh", authorize("matching.execute"), func(c *gin.Context) {
		if !pathID(c) {
			return
		}
		body, ok := readBody(c)
		if !ok {
			return
		}
		hash := str(body["expected_input_hash"])
		if len(body) != 1 || len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
			errorResponse(c, fail("INVALID_CONTRACT"))
			return
		}
		response, err := store.Refresh(c.Request.Context(), c.Param("run_id"), hash)
		if err != nil {
			errorResponse(c, err)
			return
		}
		c.JSON(200, response)
	})
	r.POST(base+"/:run_id/result", authorize("matching.execute"), func(c *gin.Context) {
		if !pathID(c) {
			return
		}
		body, ok := readBody(c)
		if !ok {
			return
		}
		response, err := store.Commit(c.Request.Context(), c.Param("run_id"), body)
		if err != nil {
			errorResponse(c, err)
			return
		}
		slog.Info("matching_result_resolved", "run_id", response["run_id"], "batch_id", response["batch_id"], "correlation_id", response["correlation_id"], "disposition", response["disposition"], "replay", response["replay"])
		c.JSON(200, response)
	})
	return nil
}
