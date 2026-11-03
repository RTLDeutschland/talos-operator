package v1alpha1

// ClusterEtcdBackupSecret is the expected data structure for a Kubernetes secret containing etcd backup credentials.
type ClusterEtcdBackupSecretData struct {
	AWSAccessKeyID     string `mapstructure:"AWS_ACCESS_KEY_ID"`          // required
	AWSSecretAccessKey string `mapstructure:"AWS_SECRET_ACCESS_KEY"`      // required
	AWSSessionToken    string `mapstructure:"AWS_SESSION_TOKEN,omitzero"` // optional
	AWSEndpoint        string `mapstructure:"AWS_ENDPOINT,omitzero"`      // default from minio client
	AWSRegion          string `mapstructure:"AWS_REGION,omitzero"`        // default from minio client
	AWSS3Bucket        string `mapstructure:"AWS_S3_BUCKET"`              // required
}

// CrossNamespaceRef is a reference with support for specifying a namespace.
type CrossNamespaceRef struct {
	// Name of the referenced resource.
	// +required
	Name string `json:"name"`

	// Namespace of the referenced resource.
	// +optional
	Namespace string `json:"namespace,omitzero"`
}

// ClusterEtcdBackup defines the desired state of the automatic etcd backup feature for a Cluster.
type ClusterEtcdBackup struct {
	// Enabled will enable the etcd backups feature.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// CredentialsRef is a reference to a secret containing the credentials used to access S3 to upload the etcd backups.
	// +required
	CredentialsSecretRef CrossNamespaceRef `json:"credentialsSecretRef"`

	// Interval specifies how often etcd backups should be performed, in duration format (e.g., "30m", "1h").
	// Defaults to every 4 hours if left unspecified.
	// +optional
	Interval string `json:"interval,omitempty"`

	// Prefix specifies a prefix to use in the S3 bucket for storing etcd backups.
	// Full object key format is <prefix>/<cluster namespace>/<cluster name>/<timestamp>.snap
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// EncryptionAgeRecipients enables https://age-encryption.org/ of the snapshot file
	// to the following list of recipients.
	// +optional
	EncryptionAgeRecipients []string `json:"encryptionAgeRecipients,omitempty"`
}
