package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v4"

	. "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
)

// networkFlags holds the network configuration flags.
type networkFlags struct {
	interfaceName string
	addresses     []string
	routes        []string
	mtu           int
}

func newNetworkFlags(cmd *cobra.Command) *networkFlags {
	nf := &networkFlags{}
	cmd.Flags().
		StringVar(&nf.interfaceName, "network-interface", "", "Network interface name (e.g., enp0s1)")
	cmd.Flags().
		StringSliceVar(&nf.addresses, "network-address", nil, "Network addresses (e.g., 192.168.2.0/24)")
	cmd.Flags().
		StringSliceVar(&nf.routes, "network-route", nil,
			"Network routes in format 'network:gateway[:metric]' (e.g., 0.0.0.0/0:192.168.2.1 or 0.0.0.0/0:192.168.2.1:1024)")
	cmd.Flags().IntVar(&nf.mtu, "network-mtu", 0, "Network interface MTU")
	return nf
}

// generateNetworkPatch generates a Talos network configuration patch YAML.
func generateNetworkPatch(nf *networkFlags) (string, error) {
	if nf == nil || nf.interfaceName == "" {
		return "", nil
	}

	patchData := KV{
		"machine": KV{
			"network": KV{
				"interfaces": []KV{
					{
						"interface": nf.interfaceName,
					},
				},
			},
		},
	}

	iface := patchData["machine"].(KV)["network"].(KV)["interfaces"].([]KV)[0]

	if len(nf.addresses) > 0 {
		iface["addresses"] = nf.addresses
	}

	if len(nf.routes) > 0 {
		routes := make([]KV, 0, len(nf.routes))
		for _, route := range nf.routes {
			// Parse route format: network:gateway[:metric]
			parts := strings.Split(route, ":")
			if len(parts) < 2 || len(parts) > 3 {
				return "", fmt.Errorf(
					"invalid route format: %s (expected network:gateway or network:gateway:metric)",
					route,
				)
			}
			routeKV := KV{
				"network": parts[0],
				"gateway": parts[1],
			}
			if len(parts) == 3 {
				metric, err := strconv.Atoi(parts[2])
				if err != nil {
					return "", fmt.Errorf("invalid metric in route: %s (must be an integer)", route)
				}
				routeKV["metric"] = metric
			}
			routes = append(routes, routeKV)
		}
		iface["routes"] = routes
	}

	if nf.mtu > 0 {
		iface["mtu"] = nf.mtu
	}

	patchYaml, err := yaml.Marshal(patchData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal network patch: %w", err)
	}

	return string(patchYaml), nil
}

func createCmdGroup() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create Talos resources",
	}
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		// test if kubectl is working
		kCmd := exec.CommandContext(cmd.Context(), "kubectl", "cluster-info")
		out, err := kCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl not working: %s: %w", string(out), err)
		}
		return nil
	}
	cmd.AddCommand(
		createClusterCmd(),
		createNodeCmd(),
	)
	return cmd
}

func createClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster NAME DOMAIN",
		Short: "Create a Talos cluster",
		Args:  cobra.ExactArgs(2),
	}

	var kubernetesVersion string
	cmd.Flags().
		StringVar(&kubernetesVersion, "kubernetes-version", "", "Kubernetes version for the cluster")
	var installImage string
	cmd.Flags().
		StringVar(&installImage, "install-image", "", "Talos image for the cluster, see https://factory.talos.dev/")
	var installDisk string
	cmd.Flags().
		StringVar(&installDisk, "install-disk", "/dev/sda", "If image was supplied, which disk to install to")
	networkFlags := newNetworkFlags(cmd)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		clusterName := args[0]
		domainName := args[1]

		secretName := fmt.Sprintf("%s-secrets", clusterName)

		// test k8s for secret's existence
		kArgs := []string{"get", "secrets", secretName}
		if *namespace != "" {
			kArgs = append([]string{"-n", *namespace}, kArgs...)
		}
		err := exec.CommandContext(cmd.Context(), "kubectl", kArgs...).Run()
		secretsExist := err == nil

		if !secretsExist {
			fmt.Println("Generating cluster secrets...")

			// generate secrets.yaml
			secretsYaml, err := exec.CommandContext(cmd.Context(), "talosctl", "gen", "secrets", "-o", "-").
				Output()
			if err != nil {
				return fmt.Errorf("failed to generate secrets: %w", err)
			}

			// pack it into a payload
			secretData := KV{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": KV{
					"name": secretName,
				},
				"stringData": KV{
					"secrets.yaml": string(secretsYaml),
				},
			}
			if *namespace != "" {
				secretData["metadata"].(KV)["namespace"] = *namespace
			}

			if err := kubectlApply(cmd.Context(), secretData); err != nil {
				return fmt.Errorf("failed to apply secrets: %w", err)
			}
		}

		data := KV{
			"apiVersion": "talos.rtl.de/v1alpha1",
			"kind":       "Cluster",
			"metadata": KV{
				"name": clusterName,
			},
			"spec": KV{
				"secretsRef": secretName,
				"domain":     domainName,
			},
		}
		if *namespace != "" {
			data["metadata"].(KV)["namespace"] = *namespace
		}
		if kubernetesVersion != "" {
			data["spec"].(KV)["kubernetesVersion"] = kubernetesVersion
		}

		patches := []string{}
		// machine install image patch:
		if installImage != "" {
			patchData := KV{
				"version": "v1alpha1",
				"machine": KV{
					"install": KV{
						"image": installImage,
						"disk":  installDisk,
					},
				},
			}
			patchYaml, err := yaml.Marshal(patchData)
			if err != nil {
				return fmt.Errorf("failed to marshal talos image patch: %w", err)
			}
			patches = append(patches, string(patchYaml))
		}
		// network patch:
		networkPatch, err := generateNetworkPatch(networkFlags)
		if err != nil {
			return fmt.Errorf("failed to generate network patch: %w", err)
		}
		if networkPatch != "" {
			patches = append(patches, networkPatch)
		}
		data["spec"].(KV)["patches"] = patches

		if err := kubectlApply(cmd.Context(), data); err != nil {
			return fmt.Errorf("failed to create cluster: %w", err)
		}
		return nil
	}
	return cmd
}

func createNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node CLUSTER NAME ROLE",
		Short: "Create a Talos node",
		Args:  cobra.ExactArgs(3),
	}
	var provisioningAddress string
	cmd.Flags().
		StringVar(&provisioningAddress, "provision-address", "",
			"Provisioning address for the node. Starts an immediate provisioning operation if set.")
	networkFlags := newNetworkFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		clusterName := args[0]
		nodeName := args[1]
		role := args[2]

		if role != NodeRoleControlplane && role != NodeRoleWorker {
			return fmt.Errorf("invalid role: %s (must be 'controlplane' or 'worker')", role)
		}

		// test that cluster exists
		kArgs := []string{"get", "clusters.talos.rtl.de", clusterName}
		if *namespace != "" {
			kArgs = append([]string{"-n", *namespace}, kArgs...)
		}
		out, err := exec.CommandContext(cmd.Context(), "kubectl", kArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("cluster %s does not exist:\n%s", clusterName, string(out))
		}

		// create node object
		data := KV{
			"apiVersion": "talos.rtl.de/v1alpha1",
			"kind":       "Node",
			"metadata": KV{
				"name": nodeName,
			},
			"spec": KV{
				"clusterRef": clusterName,
				"role":       role,
			},
		}
		if *namespace != "" {
			data["metadata"].(KV)["namespace"] = *namespace
		}

		if provisioningAddress != "" {
			data["operation"] = KV{
				"type": "Provision",
				"provision": KV{
					"address": provisioningAddress,
				},
			}
		}

		// network patch:
		patches := []string{}
		networkPatch, err := generateNetworkPatch(networkFlags)
		if err != nil {
			return fmt.Errorf("failed to generate network patch: %w", err)
		}
		if networkPatch != "" {
			patches = append(patches, networkPatch)
		}
		if len(patches) > 0 {
			data["spec"].(KV)["patches"] = patches
		}

		if err := kubectlApply(cmd.Context(), data); err != nil {
			return fmt.Errorf("failed to create node: %w", err)
		}

		return nil
	}
	return cmd
}

// kubectlApply applies the given data using kubectl.
func kubectlApply(ctx context.Context, data KV) error {
	file, err := os.CreateTemp("", "*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(file.Name())

	// serialize data to YAML and write to file
	if err := yaml.NewEncoder(file).Encode(data); err != nil {
		return fmt.Errorf("failed to write YAML to temp file: %w", err)
	}
	file.Close()

	// send it:
	args := []string{"apply", "-f", file.Name()}
	kCmd := exec.CommandContext(ctx, "kubectl", args...)
	kCmd.Stdout = os.Stdout
	kCmd.Stderr = os.Stderr
	if err := kCmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed: %w", err)
	}

	return nil
}
