package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	talosv1alpha1 "github.com/RTLDeutschland/talos-operator/api/v1alpha1"
	talosv1alpha1consts "github.com/RTLDeutschland/talos-operator/api/v1alpha1/constants"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

var programStart = time.Now()
var spinnerTick int
var spinnerOut = os.Stderr

var spinnerFrames = []string{"◰", "◳", "◲", "◱"}

const (
	ansiReset  = "\033[0m"
	ansiYellow = "\033[33m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
)

func isTerminal() bool {
	return term.IsTerminal(int(spinnerOut.Fd()))
}

func colorCode(name string) string {
	switch name {
	case "green":
		return ansiGreen
	case "red":
		return ansiRed
	default:
		return ansiYellow
	}
}

func renderSpinnerLine(text, color string) {
	frame := spinnerFrames[spinnerTick%len(spinnerFrames)]
	spinnerTick++

	fmt.Fprint(spinnerOut, "\r\033[2K")
	fmt.Fprintf(spinnerOut, "%s%s  %s%s", colorCode(color), frame, text, ansiReset)
}

func watchResource(ctx context.Context, resourceType, namespace, name string) error {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	if err != nil {
		return fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	err = talosv1alpha1.AddToScheme(scheme)
	if err != nil {
		return fmt.Errorf("failed to add talosv1alpha1 scheme: %w", err)
	}

	// load config
	cfgRules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfgOverrides := &clientcmd.ConfigOverrides{}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(cfgRules, cfgOverrides)
	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to get REST config: %w", err)
	}

	// resolve default namespace from config
	if namespace == "" {
		ns, _, err := cfg.Namespace()
		if err != nil {
			return fmt.Errorf("failed to get default namespace from kubeconfig: %w", err)
		}
		namespace = ns
	}

	crClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// block forever until the operation completes
	for {
		time.Sleep(200 * time.Millisecond)

		switch resourceType {
		case "nodes.talos.rtl.de":
			node := &talosv1alpha1.Node{}
			err = crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, node)
			if err != nil {
				return fmt.Errorf("failed to get node resource: %w", err)
			}

			op := node.Status.Operation
			if op == nil || op.LastTransitionTime.Time.Before(programStart) {
				renderSpinnerLine("waiting for operation to start...", "yellow")
				continue
			}

			lineColor := "yellow"
			lineText := fmt.Sprintf("operation %s in phase %s: %s", op.Type, op.Phase, op.Message)
			if op.Phase == talosv1alpha1consts.NodeOperationPhaseDone {
				lineColor = "green"
				lineText += "\n"
				renderSpinnerLine(lineText, lineColor)
				return nil
			}
			if op.Phase == talosv1alpha1consts.NodeOperationPhaseFailed {
				lineColor = "red"
				lineText += "\n"
				renderSpinnerLine(lineText, lineColor)
				return nil
			}

			renderSpinnerLine(lineText, lineColor)

		case "clusters.talos.rtl.de":
			cluster := &talosv1alpha1.Cluster{}
			err = crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cluster)
			if err != nil {
				return fmt.Errorf("failed to get cluster resource: %w", err)
			}

			op := cluster.Status.Operation
			if op == nil || op.LastTransitionTime.Time.Before(programStart) {
				renderSpinnerLine("waiting for operation to start...", "yellow")
				continue
			}

			lineColor := "yellow"
			lineText := fmt.Sprintf("operation %s in phase %s: %s", op.Type, op.Phase, op.Message)
			if op.Phase == talosv1alpha1consts.ClusterOperationPhaseDone {
				lineColor = "green"
				lineText += "\n"
				renderSpinnerLine(lineText, lineColor)
				return nil
			}
			if op.Phase == talosv1alpha1consts.ClusterOperationPhaseFailed {
				lineColor = "red"
				lineText += "\n"
				renderSpinnerLine(lineText, lineColor)
				return nil
			}

			renderSpinnerLine(lineText, lineColor)

		default:
			return fmt.Errorf("unsupported resource type for watch: %s", resourceType)
		}
	}
}
