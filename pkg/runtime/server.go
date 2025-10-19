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

	// Register handlers for each hook
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/generatepatches", s.handleGeneratePatches)
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclustercreate", s.handleBeforeClusterCreate)
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclusterdelete", s.handleBeforeClusterDelete)
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/afterclusterupgrade", s.handleAfterClusterUpgrade)
	mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/discovery", s.handleDiscovery)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
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
	s.logger.V(1).Info("GeneratePatches hook called")

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

	response := &runtimehooksv1.GeneratePatchesResponse{}
	s.extension.GeneratePatches(r.Context(), request, response)

	s.writeResponse(w, response)
}

func (s *Server) handleBeforeClusterCreate(w http.ResponseWriter, r *http.Request) {
	s.logger.V(1).Info("BeforeClusterCreate hook called")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.handleError(w, "failed to read request body", err)
		return
	}
	defer r.Body.Close()

	request := &runtimehooksv1.BeforeClusterCreateRequest{}
	if err := json.Unmarshal(body, request); err != nil {
		s.handleError(w, "failed to unmarshal request", err)
		return
	}

	response := &runtimehooksv1.BeforeClusterCreateResponse{}
	s.extension.BeforeClusterCreate(r.Context(), request, response)

	s.writeResponse(w, response)
}

func (s *Server) handleBeforeClusterDelete(w http.ResponseWriter, r *http.Request) {
	s.logger.V(1).Info("BeforeClusterDelete hook called")

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

	response := &runtimehooksv1.BeforeClusterDeleteResponse{}
	s.extension.BeforeClusterDelete(r.Context(), request, response)

	s.writeResponse(w, response)
}

func (s *Server) handleAfterClusterUpgrade(w http.ResponseWriter, r *http.Request) {
	s.logger.V(1).Info("AfterClusterUpgrade hook called")

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

	response := &runtimehooksv1.AfterClusterUpgradeResponse{}
	s.extension.AfterClusterUpgrade(r.Context(), request, response)

	s.writeResponse(w, response)
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	s.logger.V(1).Info("Discovery hook called")

	failPolicyFail := runtimehooksv1.FailurePolicyFail
	failPolicyIgnore := runtimehooksv1.FailurePolicyIgnore

	response := &runtimehooksv1.DiscoveryResponse{}
	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
	response.Handlers = []runtimehooksv1.ExtensionHandler{
		{
			Name: s.extension.Name() + "-generate-patches",
			RequestHook: runtimehooksv1.GroupVersionHook{
				APIVersion: runtimehooksv1.GroupVersion.String(),
				Hook:       "GeneratePatches",
			},
			TimeoutSeconds: ptrInt32(30),
			FailurePolicy:  &failPolicyFail,
		},
		{
			Name: s.extension.Name() + "-before-create",
			RequestHook: runtimehooksv1.GroupVersionHook{
				APIVersion: runtimehooksv1.GroupVersion.String(),
				Hook:       "BeforeClusterCreate",
			},
			TimeoutSeconds: ptrInt32(10),
			FailurePolicy:  &failPolicyIgnore,
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
