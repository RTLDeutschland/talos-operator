package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	"github.com/RTLDeutschland/talos-operator/internal/utils"
	"github.com/blang/semver/v4"
	tclient "github.com/siderolabs/talos/pkg/machinery/client"
	tclientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/role"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// getTalosClientConfig creates and returns a Talos client configuration for the given Node and Cluster combination.
func getTalosClientConfig(
	ctx context.Context,
	client kclient.Client,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (*tclientconfig.Config, error) {
	// fetch machine config
	secretsBundle, err := getSecrets(ctx, client, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets bundle: %w", err)
	}
	// TODO (perf): cache this?
	talosClientCert, err := secretsBundle.GenerateTalosAPIClientCertificate(
		role.MakeSet(role.Admin),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Talos API client certificate: %w", err)
	}
	talosClientConfig := tclientconfig.NewConfig(
		cluster.Name,
		[]string{node.GetFQDN(cluster)},
		secretsBundle.Certs.OS.Crt,
		talosClientCert,
	)
	return talosClientConfig, nil
}

// getTalosClient creates and returns a Talos client for the given Node and Cluster.
func getTalosClient(
	ctx context.Context,
	client kclient.Client,
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) (context.Context, *tclient.Client, error) {
	log := logz.New(ctx, "getTalosClient")

	talosClientConfig, err := getTalosClientConfig(ctx, client, node, cluster)
	if err != nil {
		return nil, nil, err
	}

	opts := make([]tclient.OptionFunc, 0, 3)
	opts = append(opts, tclient.WithConfig(talosClientConfig))

	// detect if connectHostOverride is in use and override endpoints / TLS config accordingly
	connectHost := node.GetConnectHost(cluster)
	nodeFQDN := node.GetFQDN(cluster)
	if connectHost != nodeFQDN {
		log.Trace().
			Str("connectHost", connectHost).
			Str("serverName", nodeFQDN).
			Msg("Using connect host override, setting TLS ServerName")

		// the reason we override endpoints here is that getTalosClientConfig is used for other
		// user-facing activities and we don't want our connection override ending up in
		// those generated secrets.
		opts = append(opts, tclient.WithEndpoints(connectHost))

		// mimic client/connection.go's buildTLSConfig here
		tlsConfig := &tls.Config{}

		// the reason we're all here:
		tlsConfig.ServerName = nodeFQDN

		cfgContext, ok := talosClientConfig.Contexts[talosClientConfig.Context]
		if !ok {
			return nil, nil, fmt.Errorf(
				"context %s not found in Talos client config",
				talosClientConfig.Context,
			)
		}

		caBytes, err := base64.StdEncoding.DecodeString(cfgContext.CA)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode CA certificate: %w", err)
		}
		tlsConfig.RootCAs = x509.NewCertPool()
		if ok := tlsConfig.RootCAs.AppendCertsFromPEM(caBytes); !ok {
			return nil, nil, errors.New("failed to append CA certificate to RootCAs pool")
		}

		clientCert, err := tclient.CertificateFromConfigContext(cfgContext)
		// thank you for exposing this, Talos :)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"failed to get client certificate from config context: %w",
				err,
			)
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.Certificates = append(tlsConfig.Certificates, *clientCert)

		opts = append(opts, tclient.WithTLSConfig(tlsConfig))
	}

	ctx = tclient.WithNode(ctx, connectHost)
	talosClient, err := tclient.New(
		ctx,
		opts...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Talos client: %w", err)
	}
	return ctx, talosClient, nil
}

// getNodeTalosRelease fetches the Talos version and schematic ID from the node.
func getNodeTalosRelease(
	tCtx context.Context,
	talosClient *tclient.Client,
) (semver.Version, string, error) {
	// fetch the OS release
	osReleaseReader, err := talosClient.Read(tCtx, "/etc/os-release")
	if err != nil {
		return semver.Version{}, "", fmt.Errorf("failed to read /etc/os-release: %w", err)
	}
	defer osReleaseReader.Close()
	osRelease, err := utils.ParseOSRelease(osReleaseReader)
	if err != nil {
		return semver.Version{}, "", fmt.Errorf("failed to parse /etc/os-release: %w", err)
	}
	talosVersionStr, ok := osRelease["VERSION_ID"]
	if !ok {
		return semver.Version{}, "", errors.New("VERSION_ID not found in /etc/os-release")
	}
	talosVersion, err := semver.ParseTolerant(talosVersionStr)
	if err != nil {
		return semver.Version{}, "", fmt.Errorf("failed to parse Talos version: %w", err)
	}

	// fetch the Talos extensions
	extensionsReader, err := talosClient.Read(tCtx, "/etc/extensions.yaml")
	if err != nil {
		return talosVersion, "", fmt.Errorf("failed to read /etc/extensions.yaml: %w", err)
	}
	defer extensionsReader.Close()
	extensions, err := utils.ParseExtensions(extensionsReader)
	if err != nil {
		return talosVersion, "", fmt.Errorf("failed to parse /etc/extensions.yaml: %w", err)
	}
	talosSchematicID := extensions.GetSchematicID()

	return talosVersion, talosSchematicID, nil
}

// getNodeBootID fetches the current boot ID from the node.
func getNodeBootID(
	tCtx context.Context,
	talosClient *tclient.Client,
) (string, error) {
	bootIDReader, err := talosClient.Read(tCtx, "/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("failed to read boot ID: %w", err)
	}
	defer bootIDReader.Close()
	bootIDBytes, err := io.ReadAll(bootIDReader)
	if err != nil {
		return "", fmt.Errorf("failed to read boot ID bytes: %w", err)
	}
	bootID := string(bootIDBytes)
	bootID = strings.TrimSpace(bootID)
	return bootID, nil
}

// parseImageTalosRelease tries to parse the Talos version and schematic ID from an image string.
// Schematic ID may be empty string if it cannot be parsed.
func parseImageTalosRelease(image string) (semver.Version, string, error) {
	imageRepo, imageTag, ok := strings.Cut(image, ":")
	if !ok {
		return semver.Version{}, "", fmt.Errorf("invalid image format: %s", image)
	}
	schematicID := path.Base(imageRepo)
	if len(schematicID) != 64 {
		schematicID = ""
	}
	version, err := semver.ParseTolerant(imageTag)
	if err != nil {
		return semver.Version{}, "", fmt.Errorf(
			"failed to parse image tag as semver: %w",
			err,
		)
	}
	return version, schematicID, nil
}
