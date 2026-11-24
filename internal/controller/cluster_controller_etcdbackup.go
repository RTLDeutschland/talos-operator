package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

// eventAndSetCondition simultaneously records an event and creates a condition to be passed to meta.SetStatusCondition.
func eventAndSetCondition(
	recorder events.EventRecorder,
	regarding client.Object,
	eventType string,
	eventAction string,
	conditionType string,
	reason string,
	message string,
	args ...any,
) metav1.Condition {
	message = fmt.Sprintf(message, args...)
	recorder.Eventf(
		regarding,
		nil,
		eventType,
		reason,
		eventAction,
		message,
	)
	return metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

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
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Failed to parse AWS endpoint: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %v", err)
		}
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
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Failed to create S3 client: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %v", err)
		}
		return nil, nil
	}

	// test if s3 client is working
	testKey := ".connection-test-" + rand.String(16)
	_, err = s3Client.StatObject(
		ctx,
		uploadCredentials.AWSS3Bucket,
		testKey,
		minio.StatObjectOptions{},
	)
	if err != nil && minio.IsNetworkOrHostDown(err, false) {
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonS3ConnectFailed,
				"Failed to reach S3 endpoint: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %v", err)
		}
		return nil, nil
	}

	// create temp file to store the snapshot before upload
	file, err := os.CreateTemp("", "*.snap")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for etcd snapshot: %v", err)
	}
	defer func() {
		err := file.Close()
		if err != nil {
			log.Err(err).Msg("Failed to close temporary file.")
		}
		err = os.Remove(file.Name())
		if err != nil {
			log.Err(err).Msg("Failed to remove temporary file.")
		}
	}()
	// don't close this one until we're done with S3 upload, though

	// enable outer-most age encryption layer if requested
	var fnSuffix string // file name suffix without .snap (prefixed later)
	var ageCloser io.Closer = nil
	var localWriter io.Writer = file

	encryptionConfig := ptr.Deref(
		backupConfig.Encryption,
		talosv1alpha1.ClusterEtcdBackupEncryption{Enabled: new(false)},
	)
	encryptionEnabled := ptr.Deref(
		encryptionConfig.Enabled,
		true,
	) // field default is true, if block unset then false
	ageConfig := ptr.Deref(encryptionConfig.Age, talosv1alpha1.ClusterEtcdBackupEncryptionAge{})
	if encryptionEnabled {
		if len(ageConfig.Recipients) == 0 {
			// TODO(webhook): if we ever add a validating webhook, validate this.
			err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
				meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
					r.Recorder,
					cl,
					corev1.EventTypeWarning,
					EventActionEtcdBackup,
					ClusterConditionTypeEtcdBackupFailed,
					ClusterEtcdBackupReasonValidationError,
					"Encryption was requested but no age recipients were specified",
				))
			})
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster status: %v", err)
			}
			return nil, nil
		}

		buf := new(bytes.Buffer)
		for _, rString := range ageConfig.Recipients {
			buf.WriteString(rString)
			buf.WriteString("\n")
		}
		recipients, err := age.ParseRecipients(buf)
		if err != nil {
			err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
				meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
					r.Recorder,
					cl,
					corev1.EventTypeWarning,
					EventActionEtcdBackup,
					ClusterConditionTypeEtcdBackupFailed,
					ClusterEtcdBackupReasonValidationError,
					"Failed to parse age recipients: %v",
					err,
				))
			})
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster status: %v", err)
			}
			return nil, nil
		}

		ageWriter, err := age.Encrypt(localWriter, recipients...)
		if err != nil {
			return nil, fmt.Errorf("failed to create age encryption writer: %v", err)
		}

		fnSuffix = ".age" + fnSuffix
		ageCloser = ageWriter
		localWriter = ageWriter
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

	nCopied, err := io.Copy(
		zstdCompressor,
		etcdSnapshotReader,
	)
	if err != nil {
		return nil, fmt.Errorf("error while writing etcd snapshot: %v", err)
	}

	// remember to close all of the things
	errs := make([]error, 0, 3)
	errs = append(errs, etcdSnapshotReader.Close())
	errs = append(errs, zstdCompressor.Close())
	if ageCloser != nil {
		errs = append(errs, ageCloser.Close())
	}
	if len(errs) > 0 {
		for _, e := range errs {
			if e != nil {
				return nil, fmt.Errorf("error while closing resource: %v", e)
			}
		}
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("failed to sync temporary file: %v", err)
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
	if nActual == 0 {
		return nil, fmt.Errorf("temporary file is empty after writing etcd snapshot")
	}
	nfCopiedMB := float64(nCopied) / (1024 * 1024)
	nfActualMB := float64(nActual) / (1024 * 1024)
	log.Debug().
		Msgf("File size after compression/encryption: %.1f MB -> %.1f MB (compression ratio %.1f%%)",
			nfCopiedMB,
			nfActualMB,
			(nfActualMB * 100 / nfCopiedMB),
		)

	// upload to S3
	prefix := uploadCredentials.GlobalPrefix + backupConfig.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
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
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonS3UploadFailed,
				"Failed to upload etcd snapshot to S3: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %v", err)
		}
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

	err = r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
		cl.Status.LastSuccessfulEtcdBackup = &metav1.Time{Time: time.Now()}
		meta.RemoveStatusCondition(&cl.Status.Conditions, ClusterConditionTypeEtcdBackupFailed)
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

	backupConfig := ptr.Deref(
		cluster.Spec.Options.EtcdBackup,
		talosv1alpha1.ClusterEtcdBackup{Enabled: new(false)},
	)
	enabled := ptr.Deref(backupConfig.Enabled, true) // if the block is set, default to true
	if !enabled {
		return nil, nil
	}

	backupIntervalString := lo.CoalesceOrEmpty(backupConfig.Interval, "4h")
	backupInterval, err := time.ParseDuration(backupIntervalString)
	if err != nil {
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Invalid etcd backup interval: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %w", err)
		}
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
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonSecurityError,
				"Cross-namespace backup credentials references are not allowed",
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %w", err)
		}
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
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Failed to fetch backup credentials secret: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %w", err)
		}
		return nil, nil
	}

	uploadCredentials := &talosv1alpha1.ClusterEtcdBackupSecretData{}
	stringData := make(map[string]string)
	for k, v := range secret.Data {
		stringData[k] = string(v)
	}
	err = mapstructure.Decode(stringData, uploadCredentials)
	if err != nil {
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Failed to decode backup credentials secret: %v",
				err,
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %w", err)
		}
		return nil, nil
	}

	if uploadCredentials.AWSAccessKeyID == "" || uploadCredentials.AWSSecretAccessKey == "" ||
		uploadCredentials.AWSS3Bucket == "" {
		err := r.updateClusterStatus(ctx, func(cl *talosv1alpha1.Cluster) {
			meta.SetStatusCondition(&cl.Status.Conditions, eventAndSetCondition(
				r.Recorder,
				cl,
				corev1.EventTypeWarning,
				EventActionEtcdBackup,
				ClusterConditionTypeEtcdBackupFailed,
				ClusterEtcdBackupReasonValidationError,
				"Incomplete backup credentials, missing access key, secret key, or S3 bucket",
			))
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update cluster status: %w", err)
		}
		return nil, nil
	}

	// send it
	return r.doEtcdBackup(ctx, cluster, &nodes[0], *uploadCredentials, "")
}
