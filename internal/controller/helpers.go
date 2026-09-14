package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	"go.yaml.in/yaml/v4"
)

type KV map[string]any

const (
	Controlplane = NodeRoleControlplane
	Worker       = NodeRoleWorker
)

// mustYaml marshals a map into a YAML string, panicking if the marshaling fails.
func mustYaml(in map[string]any) string {
	data, err := yaml.Marshal(in)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// mustYamlDoc marshals a map into a YAML document string, panicking if the marshaling fails.
func mustYamlDoc(in map[string]any) string {
	data, err := yaml.Marshal(in)
	if err != nil {
		panic(err)
	}
	return "---\n" + string(data)
}

// makeDefaultKubernetesVersions returns a KubernetesVersions struct populated with the Kubernetes version from the Cluster spec.
func makeDefaultKubernetesVersions(
	node *talosv1alpha1.Node,
	cluster *talosv1alpha1.Cluster,
) *talosv1alpha1.KubernetesVersions {
	versions := &talosv1alpha1.KubernetesVersions{
		Kubelet: cluster.Spec.KubernetesVersion,
	}
	if node.Spec.Role == Controlplane {
		versions.APIServer = cluster.Spec.KubernetesVersion
		versions.ControllerManager = cluster.Spec.KubernetesVersion
		versions.Scheduler = cluster.Spec.KubernetesVersion
	}
	return versions
}

// sha256Sum returns a string SHA256 checksum of the input data.
func sha256Sum(data []byte) string {
	w := sha256.New()
	w.Write(data)
	return hex.EncodeToString(w.Sum(nil))
}

// defaultRefreshInterval returns a 60 + jitter seconds duration for requeueing.
func defaultRefreshInterval() time.Duration {
	return 60*time.Second + time.Duration(rand.Float64()*float64(2*time.Second))
}
