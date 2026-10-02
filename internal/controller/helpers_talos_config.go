package controller

// This file is primarily focused on the Talos machine config generation logic.

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	"github.com/samber/lo"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/encoder"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	talossecrets "github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/types/k8s"
	"go.yaml.in/yaml/v4"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var DefaultTalosVersionContract = config.TalosVersion1_14

// driftedClock is a clock that intentionally skews behind by 5 seconds,
// so that the generated mTLS client cert start date is intentionally 5 seconds behind
// wall clock time.
type driftedClock struct{}

func (d *driftedClock) Now() time.Time {
	return time.Now().Add(-5 * time.Second)
}

var _ talossecrets.Clock = (*driftedClock)(nil)

// getSecrets fetches the *talossecrets.Bundle for a given Cluster.
func getSecrets(
	ctx context.Context,
	kclient client.Client,
	cluster *talosv1alpha1.Cluster,
) (*talossecrets.Bundle, error) {
	secret := &corev1.Secret{}
	err := kclient.Get(
		ctx,
		client.ObjectKey{
			Name:      cluster.Spec.SecretsRef,
			Namespace: cluster.Namespace,
		},
		secret,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets secret: %w", err)
	}

	data, ok := secret.Data["secrets.yaml"]
	if !ok {
		return nil, fmt.Errorf("secrets secret is missing 'secrets.yaml' key")
	}

	bundle := &talossecrets.Bundle{Clock: &driftedClock{}}
	err = yaml.Unmarshal(data, bundle)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal secrets bundle: %w", err)
	}
	err = bundle.Validate(config.TalosVersionCurrent)
	if err != nil {
		return nil, fmt.Errorf("invalid secrets bundle: %w", err)
	}

	return bundle, nil
}

func fetchPatchRefs(
	ctx context.Context,
	kclient client.Client,
	namespace string,
	withoutCrossNamespacePatchRefs bool,
	patchRefs []talosv1alpha1.PatchRef,
) ([]string, error) {
	patches := make([]string, 0, len(patchRefs))
	for _, patchRef := range patchRefs {
		if withoutCrossNamespacePatchRefs && patchRef.Namespace != "" {
			return nil, fmt.Errorf(
				"cross-namespace patch references are disabled: patch reference %q specifies namespace %q",
				patchRef.Name,
				patchRef.Namespace,
			)
		}
		var configMap corev1.ConfigMap
		err := kclient.Get(
			ctx,
			client.ObjectKey{
				Name: patchRef.Name,
				Namespace: func() string {
					if patchRef.Namespace != "" {
						return patchRef.Namespace
					}
					return namespace
				}(),
			},
			&configMap,
		)
		if err != nil {
			return nil, err
		}
		patchKey := patchRef.Key
		if patchKey == "" {
			patchKey = "patch.yaml"
		}
		patchData, ok := configMap.Data[patchKey]
		if !ok {
			return nil, fmt.Errorf(
				"patch key %q not found in ConfigMap %s/%s",
				patchKey,
				configMap.Namespace,
				configMap.Name,
			)
		}
		patches = append(patches, patchData)
	}
	return patches, nil
}

// addPatchToProvider loads a single patch in YAML and applies it on top of the given provider, returning the resulting provider.
func addPatchToProvider(cfg config.Provider, patchData string) (config.Provider, error) {
	patch, err := configpatcher.LoadPatch([]byte(patchData))
	if err != nil {
		truncated := patchData
		if len(truncated) > 200 {
			truncated = truncated[:200] + "... (truncated)"
		}
		return nil, fmt.Errorf("failed to load patch: %w (patch: %s)", err, truncated)
	}
	patcherOut, err := configpatcher.Apply(
		configpatcher.WithConfig(cfg),
		[]configpatcher.Patch{patch},
	)
	if err != nil {
		truncated := patchData
		if len(truncated) > 200 {
			truncated = truncated[:200] + "... (truncated)"
		}
		return nil, fmt.Errorf("failed to apply patch: %w (patch: %s)", err, truncated)
	}
	cfg, err = patcherOut.Config()
	if err != nil {
		return nil, fmt.Errorf("failed to get config after applying patch: %w", err)
	}
	return cfg, nil
}

// addPatchesToProvider applies multiple patches in sequence on top of the given provider, returning the resulting provider.
func addPatchesToProvider(
	src string,
	cfg config.Provider,
	patches []string,
) (config.Provider, error) {
	var err error
	for n, patchData := range patches {
		cfg, err = addPatchToProvider(cfg, patchData)
		if err != nil {
			return nil, fmt.Errorf("failed to apply %s patch #%d: %w", src, n, err)
		}
	}
	return cfg, nil
}

// GenerateMachineConfigOptions contains options for generating machine configurations.
type GenerateMachineConfigOptions struct {
	WithoutSecrets                 bool // if set, secrets will be omitted from the generated machine config
	WithoutRTLLabel                bool // whether or not to omit rtl.de/cluster-name node annotation patch
	WithoutCrossNamespacePatchRefs bool // whether or not to allow patch refs with an explicit namespace
}

// demoSecretsBundle is here to avoid generating fresh secrets only for them to be thrown away
// immediately when GenerateMachineConfig is called with the WithoutSecrets option.
var demoSecretsBundle *talossecrets.Bundle = lo.Must(
	talossecrets.NewBundle(talossecrets.NewFixedClock(time.Now()), config.TalosVersion1_11),
)

// GenerateMachineConfig assembles the machine configuration for this Node.
//
// We first combine all of the input patches so that we can later use them for heuristics when
// generating the initial patch that all the combined patches live on top of.
func GenerateMachineConfig(
	ctx context.Context,
	kclient client.Client,
	node *talosv1alpha1.Node,
	opts *GenerateMachineConfigOptions,
) (config.Provider, talosv1alpha1.PatchHierarchy, error) {
	patchHierarchy := make(talosv1alpha1.PatchHierarchy, 0, 16)

	// fetch Cluster object
	cluster := &talosv1alpha1.Cluster{}
	err := kclient.Get(
		ctx,
		client.ObjectKey{
			Name:      node.Spec.ClusterRef,
			Namespace: node.Namespace,
		},
		cluster,
	)
	if err != nil {
		return nil, patchHierarchy, err
	}

	// try to parse version contract from cluster spec, otherwise default to latest
	versionContractStr := cluster.Spec.TalosVersionContract
	var versionContract *config.VersionContract = DefaultTalosVersionContract // current is a nil pointer, oops
	if versionContractStr != "" {
		versionContract, err = config.ParseContractFromVersion(versionContractStr)
		if err != nil {
			return nil, patchHierarchy, fmt.Errorf(
				"failed to parse version contract from TalosVersionContract %q: %w",
				cluster.Spec.TalosVersionContract,
				err,
			)
		}
	}

	// initialize the generator
	clusterFQDN := fmt.Sprintf("%s.%s", cluster.GetName(), cluster.Spec.Domain)
	generateOptions := []generate.Option{
		generate.WithAdditionalSubjectAltNames([]string{clusterFQDN}),
		generate.WithVersionContract(versionContract),
	}
	// add secrets if not opted out
	if opts == nil || !opts.WithoutSecrets {
		secretsBundle, err := getSecrets(ctx, kclient, cluster)
		if err != nil {
			return nil, patchHierarchy, fmt.Errorf("failed to get secrets bundle: %w", err)
		}
		generateOptions = append(generateOptions, generate.WithSecretsBundle(secretsBundle))
	} else {
		// avoids generating secrets and wasting entropy (or if nothing else, CPU time)
		generateOptions = append(generateOptions, generate.WithSecretsBundle(demoSecretsBundle))
	}

	// generate the initial machine config
	input, err := generate.NewInput(
		cluster.Name,
		fmt.Sprintf("https://%s:6443", clusterFQDN),
		strings.TrimPrefix(cluster.Spec.KubernetesVersion, "v"),
		generateOptions...,
	)
	if err != nil {
		return nil, patchHierarchy, fmt.Errorf("failed to create generator input: %w", err)
	}

	machineType, err := machine.ParseType(node.Spec.Role)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"failed to parse machine type from node role %q: %w",
			node.Spec.Role,
			err,
		)
	}

	configProvider, err := input.Config(machineType)
	if err != nil {
		return nil, patchHierarchy, fmt.Errorf("failed to create the base config: %w", err)
	}

	versionContractSource := "cluster.spec.talosVersionContract"
	if versionContractStr == "" {
		versionContractSource = "operator defaults"
	}
	patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
		Source:    "operator:github.com/siderolabs/talos/pkg/machinery/config/generate",
		Synthetic: true,
		SyntheticExplanation: fmt.Sprintf(
			"the base config generated by the Talos API equivalent to "+
				"`talosctl gen config --talos-version=%q` (version from %s)",
			versionContract.String(),
			versionContractSource,
		),
	})

	// add Cluster referenced patches
	for i, patchRef := range cluster.Spec.PatchRefs {
		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			PatchRef: patchRef,
			Source:   fmt.Sprintf("cluster.spec.patchRefs:%d", i),
		})
	}
	clusterReferencedPatches, err := fetchPatchRefs(
		ctx,
		kclient,
		cluster.Namespace,
		opts != nil && opts.WithoutCrossNamespacePatchRefs,
		cluster.Spec.PatchRefs,
	)
	if err != nil {
		return nil, patchHierarchy, err
	}
	configProvider, err = addPatchesToProvider(
		"clusterRefs",
		configProvider,
		clusterReferencedPatches,
	)
	if err != nil {
		return nil, patchHierarchy, err
	}

	// add Cluster inline patches
	for i := range cluster.Spec.Patches {
		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			Source: fmt.Sprintf("cluster.spec.patches:%d", i),
		})
	}
	configProvider, err = addPatchesToProvider("cluster", configProvider, cluster.Spec.Patches)
	if err != nil {
		return nil, patchHierarchy, err
	}

	// add role-specific patches
	if node.Spec.Role == Controlplane {
		// references:
		for i, patchRef := range cluster.Spec.ControlPlanePatchRefs {
			patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
				PatchRef: patchRef,
				Source:   fmt.Sprintf("cluster.spec.controlPlanePatchRefs:%d", i),
			})
		}

		controlPlaneClusterReferencedPatches, err := fetchPatchRefs(
			ctx,
			kclient,
			cluster.Namespace,
			opts != nil && opts.WithoutCrossNamespacePatchRefs,
			cluster.Spec.ControlPlanePatchRefs,
		)
		if err != nil {
			return nil, patchHierarchy, err
		}

		// inlines:
		for i := range cluster.Spec.ControlPlanePatches {
			patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
				Source: fmt.Sprintf("cluster.spec.controlPlanePatches:%d", i),
			})
		}

		configProvider, err = addPatchesToProvider(
			"clusterControlPlaneRefs",
			configProvider,
			controlPlaneClusterReferencedPatches,
		)
		if err != nil {
			return nil, patchHierarchy, err
		}

		configProvider, err = addPatchesToProvider(
			"clusterControlPlane",
			configProvider,
			cluster.Spec.ControlPlanePatches,
		)
		if err != nil {
			return nil, patchHierarchy, err
		}
	}

	if node.Spec.Role == Worker {
		// references:
		for i, patchRef := range cluster.Spec.WorkerPatchRefs {
			patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
				PatchRef: patchRef,
				Source:   fmt.Sprintf("cluster.spec.workerPatchRefs:%d", i),
			})
		}

		workerClusterReferencedPatches, err := fetchPatchRefs(
			ctx,
			kclient,
			cluster.Namespace,
			opts != nil && opts.WithoutCrossNamespacePatchRefs,
			cluster.Spec.WorkerPatchRefs,
		)
		if err != nil {
			return nil, nil, err
		}

		// inlines:
		for i := range cluster.Spec.WorkerPatches {
			patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
				Source: fmt.Sprintf("cluster.spec.workerPatches:%d", i),
			})
		}

		configProvider, err = addPatchesToProvider(
			"clusterWorkerRefs",
			configProvider,
			workerClusterReferencedPatches,
		)
		if err != nil {
			return nil, nil, err
		}

		configProvider, err = addPatchesToProvider(
			"clusterWorker",
			configProvider,
			cluster.Spec.WorkerPatches,
		)
		if err != nil {
			return nil, patchHierarchy, err
		}
	}

	// add Node referenced patches
	for i, patchRef := range node.Spec.PatchRefs {
		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			PatchRef: patchRef,
			Source:   fmt.Sprintf("node.spec.patchRefs:%d", i),
		})
	}

	nodeReferencedPatches, err := fetchPatchRefs(
		ctx,
		kclient,
		node.Namespace,
		opts != nil && opts.WithoutCrossNamespacePatchRefs,
		node.Spec.PatchRefs,
	)
	if err != nil {
		return nil, patchHierarchy, err
	}

	configProvider, err = addPatchesToProvider("nodeRefs", configProvider, nodeReferencedPatches)
	if err != nil {
		return nil, patchHierarchy, err
	}

	// add Node inline patches
	for i := range node.Spec.Patches {
		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			Source: fmt.Sprintf("node.spec.patches:%d", i),
		})
	}

	configProvider, err = addPatchesToProvider("node", configProvider, node.Spec.Patches)
	if err != nil {
		return nil, patchHierarchy, err
	}

	// and finally, the autogenerated machine patch for the most important node-specific fields
	// (prevent people from crashing their nodes into each other by copy-pasting hostnames)
	patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
		Source:               "operator:node-role",
		Synthetic:            true,
		SyntheticExplanation: "a patch defining the machine type",
	})
	machinePatchData := KV{
		"version": "v1alpha1",
		"machine": KV{
			"type": node.Spec.Role,
		},
	}
	configProvider, err = addPatchToProvider(configProvider, mustYaml(machinePatchData))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add machine role patch: %w", err)
	}

	// kubernetes version patch
	k8sVersions := node.Status.KubernetesVersions
	if k8sVersions == nil {
		k8sVersions = makeDefaultKubernetesVersions(node, cluster)
	}

	// FIXME(deprecation): When dropping support for Talos < 1.14, remove all of the following fallbacks

	// kubernetes version patches:
	kubePatchData := ""

	// support both new-style and legacy-style config syntax by testing for its presence in the
	// generated config
	//
	// this works because in versionContract >= 1.14, the generator always emits modern config by
	// default, but users can still $delete patch that config and use the old style if there's
	// legitimate reason.
	v1a1Cfg := configProvider.RawV1Alpha1()

	if !(v1a1Cfg != nil && v1a1Cfg.MachineConfig != nil && v1a1Cfg.MachineConfig.MachineKubelet != nil) { // nolint:staticcheck // supporting deprecated configuration
		k8sKubeletConfig := k8s.NewKubeletConfigV1Alpha1()
		k8sKubeletConfig.KubeletImage = "ghcr.io/siderolabs/kubelet:" + k8sVersions.Kubelet
		k8sKubeletConfigBytes, err := yaml.Marshal(k8sKubeletConfig)
		if err != nil {
			return nil, patchHierarchy, fmt.Errorf("failed to marshal kubelet config: %w", err)
		}
		kubePatchData += string(k8sKubeletConfigBytes) + "\n---\n"
	} else {
		kubeletPatchData := KV{
			"version": "v1alpha1",
			"machine": KV{
				"kubelet": KV{
					"image": "ghcr.io/siderolabs/kubelet:" + k8sVersions.Kubelet,
				},
			},
		}
		kubePatchData += mustYaml(kubeletPatchData) + "\n---\n"
	}

	if node.Spec.Role == Controlplane {
		if !(v1a1Cfg != nil && v1a1Cfg.ClusterConfig != nil && v1a1Cfg.ClusterConfig.APIServerConfig != nil) { // nolint:staticcheck // supporting deprecated configuration
			k8sAPIServerConfig := k8s.NewKubeAPIServerConfigV1Alpha1()
			k8sAPIServerConfig.PodImage = "registry.k8s.io/kube-apiserver:" + k8sVersions.APIServer
			k8sAPIServerConfigBytes, err := yaml.Marshal(k8sAPIServerConfig)
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf(
					"failed to marshal api server config: %w",
					err,
				)
			}
			kubePatchData += string(k8sAPIServerConfigBytes) + "\n---\n"
		} else {
			apiServerPatchData := KV{
				"version": "v1alpha1",
				"cluster": KV{
					"apiServer": KV{
						"image": "registry.k8s.io/kube-apiserver:" + k8sVersions.APIServer,
					},
				},
			}
			kubePatchData += mustYaml(apiServerPatchData) + "\n---\n"
		}

		if !(v1a1Cfg != nil && v1a1Cfg.ClusterConfig != nil && v1a1Cfg.ClusterConfig.ControllerManagerConfig != nil) { // nolint:staticcheck // supporting deprecated configuration
			k8sControllerManagerConfig := k8s.NewKubeControllerManagerConfigV1Alpha1()
			k8sControllerManagerConfig.PodImage = "registry.k8s.io/kube-controller-manager:" + k8sVersions.ControllerManager
			k8sControllerManagerConfigBytes, err := yaml.Marshal(k8sControllerManagerConfig)
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf(
					"failed to marshal controller manager config: %w",
					err,
				)
			}
			kubePatchData += string(k8sControllerManagerConfigBytes) + "\n---\n"
		} else {
			controllerManagerPatchData := KV{
				"version": "v1alpha1",
				"cluster": KV{
					"controllerManager": KV{
						"image": "registry.k8s.io/kube-controller-manager:" + k8sVersions.ControllerManager,
					},
				},
			}
			kubePatchData += mustYaml(controllerManagerPatchData) + "\n---\n"
		}

		if !(v1a1Cfg != nil && v1a1Cfg.ClusterConfig != nil && v1a1Cfg.ClusterConfig.SchedulerConfig != nil) { // nolint:staticcheck // supporting deprecated configuration
			k8sSchedulerConfig := k8s.NewKubeSchedulerConfigV1Alpha1()
			k8sSchedulerConfig.PodImage = "registry.k8s.io/kube-scheduler:" + k8sVersions.Scheduler
			k8sSchedulerConfigBytes, err := yaml.Marshal(k8sSchedulerConfig)
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf(
					"failed to marshal scheduler config: %w",
					err,
				)
			}
			kubePatchData += string(k8sSchedulerConfigBytes) + "\n---\n"
		} else {
			schedulerPatchData := KV{
				"version": "v1alpha1",
				"cluster": KV{
					"scheduler": KV{
						"image": "registry.k8s.io/kube-scheduler:" + k8sVersions.Scheduler,
					},
				},
			}
			kubePatchData += mustYaml(schedulerPatchData) + "\n---\n"
		}

		if !(v1a1Cfg != nil && v1a1Cfg.ClusterConfig != nil && v1a1Cfg.ClusterConfig.ProxyConfig != nil) { // nolint:staticcheck // supporting deprecated configuration
			k8sKubeProxyConfig := k8s.NewKubeProxyConfigV1Alpha1()
			k8sKubeProxyConfig.ProxyImage = "registry.k8s.io/kube-proxy:" + k8sVersions.Kubelet
			k8sKubeProxyConfigBytes, err := yaml.Marshal(k8sKubeProxyConfig)
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf(
					"failed to marshal kube-proxy config: %w",
					err,
				)
			}
			kubePatchData += string(k8sKubeProxyConfigBytes) + "\n---\n"
		} else {
			kubeProxyPatchData := KV{
				"version": "v1alpha1",
				"cluster": KV{
					"proxy": KV{
						"image": "registry.k8s.io/kube-proxy:" + k8sVersions.Kubelet,
					},
				},
			}
			kubePatchData += mustYaml(kubeProxyPatchData) + "\n---\n"
		}
	}

	patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
		Source:    "node.status.kubernetesVersions",
		Synthetic: true,
		SyntheticExplanation: ("a patch defining all kubernetes components and their versions, " +
			"sourced from node.status.kubernetesVersions, " +
			"defaulted from cluster.spec.kubernetesVersion"),
	})
	configProvider, err = addPatchToProvider(configProvider, kubePatchData)
	if err != nil {
		return nil, patchHierarchy, fmt.Errorf("failed to add kube patch data: %w", err)
	}

	// detect if cluster FQDN is missing from legacy apiServer configuration, and fix it
	// (because we're v1.14+ and KubeAPIServerConfig was $patch: delete'd)
	if node.Spec.Role == Controlplane && v1a1Cfg != nil && v1a1Cfg.ClusterConfig != nil &&
		v1a1Cfg.ClusterConfig.APIServerConfig != nil &&
		len(
			v1a1Cfg.ClusterConfig.APIServerConfig.ExtraCertSANs,
		) == 0 { // nolint:staticcheck // supporting deprecated configuration
		patchData := KV{
			"version": "v1alpha1",
			"cluster": KV{
				"apiServer": KV{
					"certSANs": []string{
						clusterFQDN,
					},
				},
			},
		}
		configProvider, err = addPatchToProvider(configProvider, mustYaml(patchData))
		if err != nil {
			return nil, patchHierarchy, fmt.Errorf(
				"failed to add API server extraCertSANs patch: %w",
				err,
			)
		}

		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			Source:               "operator:api-server-extra-cert-sans",
			Synthetic:            true,
			SyntheticExplanation: "a patch fixing empty SANs in the legacy API server configuration",
		})
	}

	// add cluster-name label
	if opts == nil || !opts.WithoutRTLLabel {
		patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
			Source:               "operator:cluster-name-label",
			Synthetic:            true,
			SyntheticExplanation: "a patch adding the rtl.de/cluster-name label to the node",
		})

		// FIXME(deprecation): When dropping support for Talos < 1.14
		// detect if we should use old-style syntax, assuming old-style syntax is present:
		raw := configProvider.RawV1Alpha1()
		if raw != nil && raw.MachineConfig != nil && raw.MachineConfig.MachineNodeLabels != nil &&
			len(raw.MachineConfig.MachineNodeLabels) > 0 { // nolint:staticcheck
			nodeLabelPatchData := KV{
				"version": "v1alpha1",
				"machine": KV{
					"nodeLabels": KV{
						"rtl.de/cluster-name": node.Spec.ClusterRef,
					},
				},
			}
			configProvider, err = addPatchToProvider(configProvider, mustYaml(nodeLabelPatchData))
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf("failed to add node label patch: %w", err)
			}
		} else {
			nodeConfig := k8s.NewKubeNodeConfigV1Alpha1()
			if nodeConfig.LabelsConfig == nil {
				nodeConfig.LabelsConfig = make(map[string]string)
			}
			nodeConfig.LabelsConfig["rtl.de/cluster-name"] = node.Spec.ClusterRef
			data, err := yaml.Marshal(nodeConfig)
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf(
					"failed to marshal node label patch: %w",
					err,
				)
			}
			configProvider, err = addPatchToProvider(configProvider, string(data))
			if err != nil {
				return nil, patchHierarchy, fmt.Errorf("failed to add node label patch: %w", err)
			}
		}
	}

	// detect if the user is using the multi-doc hostname format or not, and add the appropriate hostname patch accordingly
	var hostnamePatchData KV
	if v1a1Cfg != nil && v1a1Cfg.MachineConfig != nil &&
		v1a1Cfg.MachineConfig.MachineNetwork != nil &&
		v1a1Cfg.MachineConfig.MachineNetwork.NetworkHostname != "" { // nolint:staticcheck // supporting deprecated configuration
		hostnamePatchData = KV{
			"version": "v1alpha1",
			"machine": KV{
				"network": KV{
					"hostname": node.GetFQDN(cluster),
				},
			},
		}
	} else {
		hostnamePatchData = KV{
			"apiVersion": "v1alpha1",
			"kind":       "HostnameConfig",
			"auto":       "off",
			"hostname":   node.GetFQDN(cluster),
		}
	}
	patchHierarchy = append(patchHierarchy, talosv1alpha1.PatchHierarchyElement{
		Source:               "operator:hostname",
		Synthetic:            true,
		SyntheticExplanation: "a patch defining the machine hostname",
	})
	configProvider, err = addPatchToProvider(configProvider, mustYaml(hostnamePatchData))
	if err != nil {
		return nil, patchHierarchy, fmt.Errorf("failed to add hostname patch: %w", err)
	}

	return configProvider, patchHierarchy, nil
}

// EncodeMachineConfig encodes the given machine config into YAML format, optionally re-encoding with our own formatting.
func EncodeMachineConfig(cfg config.Provider, pretty bool) ([]byte, error) {
	configBytes, err := cfg.EncodeBytes(encoder.WithComments(encoder.CommentsDisabled))
	if err != nil {
		return nil, fmt.Errorf("failed to serialize final config: %w", err)
	}
	if !pretty {
		return configBytes, nil
	}

	// because we're nitpicky: re-encode with indent 2
	configReader := bytes.NewBuffer(configBytes)
	dec := yaml.NewDecoder(configReader)
	documents := make([]*yaml.Node, 0, 4)
	for {
		doc := &yaml.Node{}
		err := dec.Decode(doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("failed to decode final config into YAML documents: %w", err)
		}
		documents = append(documents, doc)
	}

	outData := &bytes.Buffer{}
	enc := yaml.NewEncoder(outData)
	enc.SetIndent(2)
	for _, doc := range documents {
		if err := enc.Encode(doc); err != nil {
			return nil, fmt.Errorf("failed to re-encode final config document: %w", err)
		}
	}
	err = enc.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to finalize re-encoding of final config: %w", err)
	}
	data := outData.Bytes()

	return data, nil
}

var pemRe = regexp.MustCompile(`-----BEGIN.*\n(?: *[A-Za-z0-9+/]+={0,2}\n)+ *-----END.*`)
var tokenRe = regexp.MustCompile(`(token): [a-z0-9]+\.[a-z0-9]+`)
var passphraseRe = regexp.MustCompile(`(passphrase): [^\n]+`) // disk encryption
var base64Re = regexp.MustCompile(`: [A-Za-z0-9+/]{43,}={0,2}`)

// RedactMachineConfig tries to remove all the secrets from the given machine config.
//
// It is recommended to combine this with `WithoutSecrets: true` so the secrets are demo secrets
// to begin with.
func RedactMachineConfig(cfg string) string {
	out := cfg
	out = pemRe.ReplaceAllString(out, "<redacted>")
	out = tokenRe.ReplaceAllString(out, "$1: <redacted>")
	out = passphraseRe.ReplaceAllString(out, "$1: <redacted>")
	out = base64Re.ReplaceAllString(out, ": <redacted>")
	return out
}

func GetInstallImage(cfg config.Provider) (string, error) {
	if cfg.UnattendedInstallConfig() != nil &&
		cfg.UnattendedInstallConfig().InstallerImage() != "" {
		return cfg.UnattendedInstallConfig().InstallerImage(), nil
	}
	if cfg.Machine() != nil && cfg.Machine().Install() != nil &&
		cfg.Machine().Install().Image() != "" {
		return cfg.Machine().Install().Image(), nil
	}
	return "", fmt.Errorf("no install image found")
}
