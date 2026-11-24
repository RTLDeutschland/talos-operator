package constants

const (
	// etcd backup conditions:
	ClusterConditionTypeEtcdBackupFailed = "EtcdBackupFailed"

	// etcd backup reasons:
	ClusterEtcdBackupReasonS3ConnectFailed = "S3ConnectFailed"
	ClusterEtcdBackupReasonS3UploadFailed  = "S3UploadFailed"
	ClusterEtcdBackupReasonValidationError = "ValidationError"
	ClusterEtcdBackupReasonSecurityError   = "SecurityError"
)
