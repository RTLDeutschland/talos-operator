// Package logz provides a zerolog Logger with some helpers for controller-gen architecture.
package logz

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	ctrl "sigs.k8s.io/controller-runtime"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/internal/constants"
)

var saneDefaultLogger = zerolog.New(os.Stdout).With().Timestamp().Logger()
var LoggerNameKey = "logger"

// New creates a new zerolog Logger and supplements some environment fields from context.
func New(ctx context.Context, name string) zerolog.Logger {
	logger := saneDefaultLogger.With().Str(LoggerNameKey, name).Logger()

	// add cluster and node info to logger if available in context for better log correlation
	var haveMeta bool = false
	node, ok := ctx.Value(CtxKeyNode).(*talosv1alpha1.Node)
	if ok && node != nil {
		logger = logger.With().
			Str("node.namespace", node.Namespace).
			Str("node.name", node.Name).
			Logger()
		haveMeta = true
	}
	cluster, ok := ctx.Value(CtxKeyCluster).(*talosv1alpha1.Cluster)
	if ok && cluster != nil {
		logger = logger.With().
			Str("cluster.namespace", cluster.Namespace).
			Str("cluster.name", cluster.Name).
			Logger()
		haveMeta = true
	}

	if !haveMeta {
		// no metadata, attach the raw request instead
		if req, ok := ctx.Value(CtxKeyRequest).(*ctrl.Request); ok && req != nil {
			logger = logger.With().
				Str("request.namespace", req.Namespace).
				Str("request.name", req.Name).
				Logger()
		}
	}

	return logger
}
