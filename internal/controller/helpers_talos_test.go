package controller

import (
	"context"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
)

func TestParseImageTalosRelease(t *testing.T) {
	// test happy path
	version, schematicID, err := parseImageTalosRelease(
		"factory.talos.dev/metal-installer/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba:v1.12.6",
	)
	require.NoError(t, err)
	require.Equal(
		t,
		"376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba",
		schematicID,
	)
	v := semver.MustParse("1.12.6")
	require.True(t, version.Equals(v))

	// test third-party Talos build
	version, schematicID, err = parseImageTalosRelease(
		"internal-registry.example/talos/installer/talos-installer:v1.12.6-rc0+build.12345",
	)
	require.NoError(t, err)
	require.Equal(t, "", schematicID)
	v = semver.MustParse("1.12.6-rc0+build.12345")
	require.True(t, version.Equals(v))

	// test invalid images
	images := []string{
		"invalid-image",
		"docker.io/library/nginx:latest",
		"",
	}
	for _, img := range images {
		version, schematicID, err = parseImageTalosRelease(img)
		require.Error(t, err)
		require.Equal(t, "", schematicID)
		require.True(t, version.Equals(semver.Version{}))
	}
}

func TestFetchPatchRefsCrossNamespaceGuard(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	kclient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-patches",
				Namespace: "patches",
			},
			Data: map[string]string{
				"patch.yaml": "version: v1alpha1\nmachine:\n  kubelet:\n    image: example.invalid/kubelet:v1\n",
			},
		},
	).Build()

	t.Run("allows namespace when enabled", func(t *testing.T) {
		patches, err := fetchPatchRefs(
			context.Background(),
			kclient,
			"default",
			false,
			[]talosv1alpha1.PatchRef{{Name: "shared-patches", Namespace: "patches"}},
		)
		require.NoError(t, err)
		require.Len(t, patches, 1)
		require.Contains(t, patches[0], "example.invalid/kubelet:v1")
	})

	t.Run("rejects namespace when disabled", func(t *testing.T) {
		patches, err := fetchPatchRefs(
			context.Background(),
			kclient,
			"default",
			true,
			[]talosv1alpha1.PatchRef{{Name: "shared-patches", Namespace: "patches"}},
		)
		require.Error(t, err)
		require.Nil(t, patches)
		require.Contains(t, err.Error(), "cross-namespace patch references are disabled")
	})
}
