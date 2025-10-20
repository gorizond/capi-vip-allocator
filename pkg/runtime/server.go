package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	runtimehooksv1 "sigs.k8s.io/cluster-api/exp/runtime/hooks/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Server wraps the Runtime Extension HTTP server.
type Server struct {
	extension *VIPExtension
	logger    logr.Logger
	port      int
	certDir   string
}

// NewServer creates a new Runtime Extension server.
func NewServer(client client.Client, logger logr.Logger, port int, certDir string, extensionName string) *Server {
	return &Server{
		extension: NewVIPExtension(client, logger, extensionName),
		logger:    logger,
		port:      port,
		certDir:   certDir,
	}
}

// Start starts the runtime extension HTTP server.
// Implements manager.Runnable interface.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Register handlers for each hook with full paths including handler names
	// CAPI Runtime SDK appends handler name to the hook path
	// v0.4.0: GeneratePatches is the ONLY hook (allocates VIP + patches Cluster + patches InfraCluster)
	//         BeforeClusterCreate removed (cannot modify Cluster - CAPI ignores request.Cluster changes)
	handlerName := s.extension.Name()
	mux.HandleFunc(fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/generatepatches/%s-generate-patches", handlerName), s.handleGeneratePatches)
	mux.HandleFunc(fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclusterdelete/%s-before-delete", handlerName), s.handleBeforeClusterDelete)
	mux.HandleFunc(fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/afterclusterupgrade/%s-after-upgrade", handlerName), s.handleAfterClusterUpgrade)
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/discovery", s.handleDiscovery)

	// Add root handler for health checks
	mux.HandleFunc("/", s.handleRoot)

	s.logger.Info("registered runtime extension handlers (v0.4.0 - GeneratePatches only)",
		"generatePatches", fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/generatepatches/%s-generate-patches", handlerName),
		"beforeDelete", fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclusterdelete/%s-before-delete", handlerName),
		"afterUpgrade", fmt.Sprintf("/hooks.runtime.cluster.x-k8s.io/v1alpha1/afterclusterupgrade/%s-after-upgrade", handlerName))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.loggingMiddleware(mux),
	}

	s.logger.Info("starting runtime extension server", "port", s.port, "certDir", s.certDir)

	// Shutdown server when context is done
	go func() {
		<-ctx.Done()
		s.logger.Info("shutting down runtime extension server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			s.logger.Error(err, "error shutting down runtime extension server")
		}
	}()

	// Start server with TLS (blocking)
	certFile := s.certDir + "/tls.crt"
	keyFile := s.certDir + "/tls.key"

	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("runtime extension server error: %w", err)
	}

	return nil
}

// NeedLeaderElection returns false as the runtime extension server doesn't need leader election.
func (s *Server) NeedLeaderElection() bool {
	return false
}

func (s *Server) handleGeneratePatches(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("GeneratePatches hook called")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.handleError(w, "failed to read request body", err)
		return
	}
	defer r.Body.Close()

	request := &runtimehooksv1.GeneratePatchesRequest{}
	if err := json.Unmarshal(body, request); err != nil {
		s.handleError(w, "failed to unmarshal request", err)
		return
	}

	s.logger.Info("GeneratePatches request decoded", "itemsCount", len(request.Items))

	response := &runtimehooksv1.GeneratePatchesResponse{}
	s.extension.GeneratePatches(r.Context(), request, response)

	s.logger.Info("GeneratePatches response prepared", "status", response.GetStatus(), "patchesCount", len(response.Items))
	s.writeResponse(w, response)
}

// handleBeforeClusterCreate - REMOVED in v0.4.0
// BeforeClusterCreate hook cannot modify Cluster object, removed completely

func (s *Server) handleBeforeClusterDelete(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("BeforeClusterDelete hook called")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.handleError(w, "failed to read request body", err)
		return
	}
	defer r.Body.Close()

	request := &runtimehooksv1.BeforeClusterDeleteRequest{}
	if err := json.Unmarshal(body, request); err != nil {
		s.handleError(w, "failed to unmarshal request", err)
		return
	}

	clusterKey := fmt.Sprintf("%s/%s", request.Cluster.Namespace, request.Cluster.Name)
	s.logger.Info("BeforeClusterDelete request decoded", "cluster", clusterKey)

	response := &runtimehooksv1.BeforeClusterDeleteResponse{}
	s.extension.BeforeClusterDelete(r.Context(), request, response)

	s.logger.Info("BeforeClusterDelete response prepared", "cluster", clusterKey, "status", response.GetStatus())
	s.writeResponse(w, response)
}

func (s *Server) handleAfterClusterUpgrade(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("AfterClusterUpgrade hook called")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.handleError(w, "failed to read request body", err)
		return
	}
	defer r.Body.Close()

	request := &runtimehooksv1.AfterClusterUpgradeRequest{}
	if err := json.Unmarshal(body, request); err != nil {
		s.handleError(w, "failed to unmarshal request", err)
		return
	}

	clusterKey := fmt.Sprintf("%s/%s", request.Cluster.Namespace, request.Cluster.Name)
	s.logger.Info("AfterClusterUpgrade request decoded", "cluster", clusterKey)

	response := &runtimehooksv1.AfterClusterUpgradeResponse{}
	s.extension.AfterClusterUpgrade(r.Context(), request, response)

	s.logger.Info("AfterClusterUpgrade response prepared", "cluster", clusterKey, "status", response.GetStatus())
	s.writeResponse(w, response)
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("Discovery hook called")

	failPolicyFail := runtimehooksv1.FailurePolicyFail
	failPolicyIgnore := runtimehooksv1.FailurePolicyIgnore

	response := &runtimehooksv1.DiscoveryResponse{}
	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
	// v0.4.0: GeneratePatches is the ONLY hook for VIP allocation (BeforeClusterCreate removed)
	response.Handlers = []runtimehooksv1.ExtensionHandler{
		{
			Name: s.extension.Name() + "-generate-patches",
			RequestHook: runtimehooksv1.GroupVersionHook{
				APIVersion: runtimehooksv1.GroupVersion.String(),
				Hook:       "GeneratePatches",
			},
			TimeoutSeconds: ptrInt32(30), // Increased to 30s for VIP allocation + patching
			FailurePolicy:  &failPolicyFail,
		},
		{
			Name: s.extension.Name() + "-before-delete",
			RequestHook: runtimehooksv1.GroupVersionHook{
				APIVersion: runtimehooksv1.GroupVersion.String(),
				Hook:       "BeforeClusterDelete",
			},
			TimeoutSeconds: ptrInt32(10),
			FailurePolicy:  &failPolicyIgnore,
		},
		{
			Name: s.extension.Name() + "-after-upgrade",
			RequestHook: runtimehooksv1.GroupVersionHook{
				APIVersion: runtimehooksv1.GroupVersion.String(),
				Hook:       "AfterClusterUpgrade",
			},
			TimeoutSeconds: ptrInt32(10),
			FailurePolicy:  &failPolicyIgnore,
		},
	}

	s.writeResponse(w, response)
}

func (s *Server) handleError(w http.ResponseWriter, message string, err error) {
	s.logger.Error(err, message)
	http.Error(w, fmt.Sprintf("%s: %v", message, err), http.StatusBadRequest)
}

func (s *Server) writeResponse(w http.ResponseWriter, response interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error(err, "failed to encode response")
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func ptrInt32(i int32) *int32 {
	return &i
}

// loggingMiddleware logs all incoming HTTP requests.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		s.logger.Info("runtime-extension: incoming HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"remoteAddr", r.RemoteAddr,
			"userAgent", r.UserAgent())

		// Create a response writer wrapper to capture status code
		wrapped := &responseWriterWrapper{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)
		s.logger.Info("runtime-extension: HTTP request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"duration", duration.String())
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// handleRoot handles requests to the root path (for health checks).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.logger.Info("runtime-extension: 404 not found", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","service":"capi-vip-allocator-runtime-extension"}`)
}
