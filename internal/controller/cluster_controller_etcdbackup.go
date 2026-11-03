package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	_ "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"github.com/RTLDeutschland/talos-operator/internal/logz"
	"github.com/go-viper/mapstructure/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

// doEtcdBackup runs the actual etcd backup process.
func (r *ClusterReconciler) doEtcdBackup(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
	controlplane *talosv1alpha1.Node,
	uploadCredentials talosv1alpha1.ClusterEtcdBackupSecretData,
	reason string,
) (*ctrl.Result, error) {
	backupConfig := ptr.Deref(cluster.Spec.Options.EtcdBackup, talosv1alpha1.ClusterEtcdBackup{})

	log := logz.New(ctx, "cluster.backup")

	log.Info().Msg("Starting etcd backup...")

	parsed, err := url.Parse(uploadCredentials.AWSEndpoint)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionEtcdBackup,
			"Failed to parse AWS endpoint: %v",
			err,
		)
		return nil, nil
	}

	s3Client, err := minio.New(uploadCredentials.AWSEndpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			uploadCredentials.AWSAccessKeyID,
			uploadCredentials.AWSSecretAccessKey,
			uploadCredentials.AWSSessionToken,
		),
		Region: uploadCredentials.AWSRegion,
		Secure: parsed.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %v", err)
	}

	// create temp file to store the snapshot before upload
	file, err := os.CreateTemp("", "*.snap")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for etcd snapshot: %v", err)
	}
	defer file.Close()
	defer func() {
		err := os.Remove(file.Name())
		if err != nil {
			log.Err(err).Msg("Failed to remove temporary file.")
		}
	}()

	closers := make([]io.Closer, 0, 3)
	closers = append(closers, file)

	var localWriter io.Writer = file
	fnSuffix := "" // file name suffix without .snap (prefixed later)

	// enable outer-most age encryption layer if requested
	if len(backupConfig.EncryptionAgeRecipients) > 0 {
		buf := new(bytes.Buffer)
		for _, rString := range backupConfig.EncryptionAgeRecipients {
			buf.WriteString(rString)
			buf.WriteString("\n")
		}

		recipients, err := age.ParseRecipients(buf)
		if err != nil {
			r.Recorder.Eventf(
				cluster,
				nil,
				corev1.EventTypeWarning,
				EventReasonValidationError,
				EventActionEtcdBackup,
				"Failed to parse age recipients: %v",
				err,
			)
			return nil, nil
		}

		ageWriter, err := age.Encrypt(localWriter, recipients...)
		if err != nil {
			return nil, fmt.Errorf("failed to create age encryption writer: %v", err)
		}
		closers = append(closers, ageWriter)
		localWriter = ageWriter
		fnSuffix = ".age"
	}

	zstdCompressor, err := zstd.NewWriter(
		localWriter,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)),
		zstd.WithConcurrentBlocks(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd compressor: %v", err)
	}
	defer zstdCompressor.Close()
	closers = append(closers, zstdCompressor)
	localWriter = zstdCompressor
	fnSuffix = ".zst" + fnSuffix // zstd *before* encryption

	tCtx, talosClient, err := getTalosClient(ctx, r, controlplane, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos client: %v", err)
	}
	defer talosClient.Close()
	etcdSnapshotReader, err := talosClient.EtcdSnapshot(tCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to take etcd snapshot: %v", err)
	}
	defer etcdSnapshotReader.Close()
	closers = append(closers, etcdSnapshotReader)

	nCopied, err := io.Copy(
		zstdCompressor,
		etcdSnapshotReader,
	)
	if err != nil {
		return nil, fmt.Errorf("error while writing etcd snapshot: %v", err)
	}
	log.Debug().
		Int64("bytes_copied", nCopied).
		Err(err).
		Msg("Finished copying etcd snapshot to localWriter.")

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat temporary file: %v", err)
	}
	nActual := stat.Size()
	nfCopiedMB := float64(nCopied) / (1024 * 1024)
	nfActualMB := float64(nActual) / (1024 * 1024)
	log.Debug().
		Msgf("File size after compression/encryption: %.1f MB -> %.1f MB (compression ratio %.1f%%)",
			nfCopiedMB,
			nfActualMB,
			(nfActualMB * 100 / nfCopiedMB),
		)

	// close all the closers in reverse order
	for _, closer := range slices.Backward(closers) {
		if err := closer.Close(); err != nil {
			return nil, fmt.Errorf("failed to close resource: %v", err)
		}
	}

	// upload to S3
	prefix := backupConfig.Prefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if reason != "" {
		reason = "-" + reason
	}
	objectKey := fmt.Sprintf(
		"%s%s/%s/%s%s.snap%s",
		prefix,
		cluster.Namespace,
		cluster.Name,
		time.Now().Format(time.RFC3339),
		reason,
		fnSuffix,
	)

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek to start of temporary file: %v", err)
	}
	info, err := s3Client.PutObject(
		ctx,
		uploadCredentials.AWSS3Bucket,
		objectKey,
		file,
		stat.Size(),
		minio.PutObjectOptions{},
	)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonFailed,
			EventActionEtcdBackup,
			"Failed to upload etcd snapshot to S3: %v (will retry)",
			err,
		)
		return nil, nil
	}

	// bump cluster status on success
	r.Recorder.Eventf(
		cluster,
		nil,
		corev1.EventTypeNormal,
		EventReasonCreated,
		EventActionEtcdBackup,
		"Successfully created etcd snapshot in S3: %s/%s (%.1f MB, compression ratio %.1f%%)",
		info.Bucket,
		info.Key,
		nfActualMB,
		(nfActualMB * 100 / nfCopiedMB),
	)

	err = r.updateClusterStatus(ctx, func(c *talosv1alpha1.Cluster) {
		c.Status.LastSuccessfulEtcdBackup = &metav1.Time{Time: time.Now()}
	})
	if err != nil {
		return nil, fmt.Errorf(
			"error updating cluster status after successful etcd backup: %v",
			err,
		)
	}

	return nil, nil
}

// reconcileEtcdBackups handles periodic etcd backup scheduling.
func (r *ClusterReconciler) reconcileEtcdBackups(
	ctx context.Context,
	cluster *talosv1alpha1.Cluster,
) (*ctrl.Result, error) {
	// don't run backups during cluster ops
	clusterOp := ptr.Deref(cluster.Status.Operation, talosv1alpha1.ClusterOperationStatus{})
	if clusterOp.Phase != ClusterOperationPhaseDone &&
		clusterOp.Phase != ClusterOperationPhaseFailed {
		return nil, nil
	}

	if cluster.Spec.Options.EtcdBackup == nil {
		return nil, nil
	}

	backupConfig := ptr.Deref(cluster.Spec.Options.EtcdBackup, talosv1alpha1.ClusterEtcdBackup{})
	enabled := ptr.Deref(backupConfig.Enabled, true) // if the block is set, default to true
	if !enabled {
		return nil, nil
	}

	backupIntervalString := lo.CoalesceOrEmpty(backupConfig.Interval, "4h")
	backupInterval, err := time.ParseDuration(backupIntervalString)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionEtcdBackup,
			"Invalid etcd backup interval: %v",
			err,
		)
		return nil, nil
	}
	lastSuccessfulEtcdBackup := ptr.Deref(
		cluster.Status.LastSuccessfulEtcdBackup,
		metav1.NewTime(time.Time{}),
	)
	if time.Since(lastSuccessfulEtcdBackup.Time) < backupInterval {
		return nil, nil
	}

	// find a controlplane to fetch the etcd snapshot from
	nodes, err := listEligibleNodes(r, ctx, cluster, Controlplane)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	if !r.FeatureFlags.EnableCrossNamespaceBackupCredentialsRefs &&
		backupConfig.CredentialsSecretRef.Namespace != "" &&
		backupConfig.CredentialsSecretRef.Namespace != cluster.Namespace {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonSecurityError,
			EventActionEtcdBackup,
			"Cross-namespace backup credentials references are not allowed",
		)
		return nil, nil
	}

	// fetch the upload credentials secret
	secret := &corev1.Secret{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: lo.CoalesceOrEmpty(
			backupConfig.CredentialsSecretRef.Namespace,
			cluster.Namespace,
		),
		Name: backupConfig.CredentialsSecretRef.Name,
	}, secret)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionEtcdBackup,
			"Failed to fetch backup credentials secret: %v",
			err,
		)
		return nil, nil
	}

	uploadCredentials := &talosv1alpha1.ClusterEtcdBackupSecretData{}
	stringData := make(map[string]string)
	for k, v := range secret.Data {
		stringData[k] = string(v)
	}
	err = mapstructure.Decode(stringData, uploadCredentials)
	if err != nil {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionEtcdBackup,
			"Failed to decode backup credentials secret: %v",
			err,
		)
		return nil, nil
	}

	if uploadCredentials.AWSAccessKeyID == "" || uploadCredentials.AWSSecretAccessKey == "" ||
		uploadCredentials.AWSS3Bucket == "" {
		r.Recorder.Eventf(
			cluster,
			nil,
			corev1.EventTypeWarning,
			EventReasonValidationError,
			EventActionEtcdBackup,
			"Incomplete backup credentials, missing access key, secret key, or S3 bucket",
		)
		return nil, nil
	}

	// send it
	return r.doEtcdBackup(ctx, cluster, &nodes[0], *uploadCredentials, "")
}
