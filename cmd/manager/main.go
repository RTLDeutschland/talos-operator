package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	talosrtldev1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	iapi "github.com/RTLDeutschland/talos-operator/internal/api"
	"github.com/RTLDeutschland/talos-operator/internal/controller"
	"github.com/RTLDeutschland/talos-operator/internal/keyedmutex"
	"github.com/RTLDeutschland/talos-operator/internal/logz"

	// +kubebuilder:scaffold:imports

	"net/http"
	_ "net/http/pprof"
)

// linker flags:
var (
	Version string
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

type managerOptions struct {
	metricsAddr          string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	enableLeaderElection bool
	probeAddr            string
	secureMetrics        bool
	enableHTTP2          bool
	enablePProf          bool
	featureFlags         iapi.FeatureFlags
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(talosrtldev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func rootCommand() *cobra.Command {
	options := &managerOptions{}
	goFlags := flag.NewFlagSet("manager", flag.ContinueOnError)
	goFlags.StringVar(
		&options.metricsAddr,
		"metrics-bind-address",
		"0",
		"The address the metrics endpoint binds to. "+
			"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.",
	)
	goFlags.StringVar(
		&options.probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	goFlags.BoolVar(&options.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	goFlags.BoolVar(
		&options.secureMetrics,
		"metrics-secure",
		true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.",
	)
	goFlags.StringVar(&options.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	goFlags.StringVar(
		&options.metricsCertName,
		"metrics-cert-name",
		"tls.crt",
		"The name of the metrics server certificate file.",
	)
	goFlags.StringVar(
		&options.metricsCertKey,
		"metrics-cert-key",
		"tls.key",
		"The name of the metrics server key file.",
	)
	goFlags.BoolVar(&options.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
	goFlags.BoolVar(&options.enablePProf, "enable-pprof", false,
		"Enables a pprof heap debugging server at 0.0.0.0:6060")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(goFlags)

	cmd := &cobra.Command{
		Use:     "manager",
		Short:   "main binary for the Talos operator",
		Version: Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runManager(*options)
		},
	}
	cmd.Flags().AddGoFlagSet(goFlags)
	cmd.Flags().
		BoolVar(&options.featureFlags.EnableRTLNodeLabel, "enable-node-label", true, "Enables the \"rtl.de/cluster-name\" label generated into machine configuration.")
	cmd.Flags().
		BoolVar(&options.featureFlags.EnableCrossNamespacePatchRefs, "enable-cross-namespace-patch-refs", true, "Enables patch references from a different namespace than the Node or Cluster resource.")

	return cmd
}

// nolint:gocyclo
func runManager(options managerOptions) error {
	ctrl.SetLogger(logr.New(&logz.LogrSink{Logger: logz.New(context.TODO(), "manager")}))

	var tlsOpts []func(*tls.Config)

	if options.enablePProf {
		// start pprof
		go func() {
			err := http.ListenAndServe("0.0.0.0:6060", nil)
			if err != nil {
				setupLog.Error(err, "unable to start pprof server")
			}
		}()
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !options.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   options.metricsAddr,
		SecureServing: options.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if options.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(options.metricsCertPath) > 0 {
		setupLog.Info(
			"Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path",
			options.metricsCertPath,
			"metrics-cert-name",
			options.metricsCertName,
			"metrics-cert-key",
			options.metricsCertKey,
		)

		metricsServerOptions.CertDir = options.metricsCertPath
		metricsServerOptions.CertName = options.metricsCertName
		metricsServerOptions.KeyName = options.metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: options.probeAddr,
		LeaderElection:         options.enableLeaderElection,
		LeaderElectionID:       "talos-operator.ac.rtl.de",
		// LeaderElectionNamespace: "kube-system",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	ctx := ctrl.SetupSignalHandler()
	cache := mgr.GetCache()
	nodeClusterRefIndexer := func(obj client.Object) []string {
		return []string{obj.(*talosrtldev1alpha1.Node).Spec.ClusterRef}
	}
	nodeRoleIndexer := func(obj client.Object) []string {
		return []string{obj.(*talosrtldev1alpha1.Node).Spec.Role}
	}
	if err := cache.IndexField(
		ctx,
		&talosrtldev1alpha1.Node{},
		"spec.clusterRef",
		nodeClusterRefIndexer,
	); err != nil {
		return fmt.Errorf("unable to create index for field spec.clusterRef: %w", err)
	}
	if err := cache.IndexField(
		ctx,
		&talosrtldev1alpha1.Node{},
		"spec.role",
		nodeRoleIndexer,
	); err != nil {
		return fmt.Errorf("unable to create index for field spec.role: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("unable to create clientset for event broadcaster: %w", err)
	}
	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: clientset.EventsV1()})
	if err := broadcaster.StartRecordingToSinkWithContext(ctx); err != nil {
		return fmt.Errorf("unable to start event broadcaster: %w", err)
	}
	defer broadcaster.Shutdown()

	// set up logging for the event broadcaster
	_, err = broadcaster.StartLogging(ctrl.Log.WithName("events"))
	if err != nil {
		return fmt.Errorf("unable to start event logging: %w", err)
	}

	mutexMap := keyedmutex.NewMutexMap()
	if err := (&controller.ClusterReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		Scheme:       mgr.GetScheme(),
		Mutexes:      mutexMap,
		Recorder:     broadcaster.NewRecorder(mgr.GetScheme(), "talos-cluster-controller"),
		FeatureFlags: options.featureFlags,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller Cluster: %w", err)
	}
	if err := (&controller.NodeReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		Scheme:       mgr.GetScheme(),
		Mutexes:      mutexMap,
		Recorder:     broadcaster.NewRecorder(mgr.GetScheme(), "talos-node-controller"),
		FeatureFlags: options.featureFlags,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller Node: %w", err)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}

	return nil
}

func main() {
	if err := rootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
