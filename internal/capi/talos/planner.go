// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

// PlanInput is everything the planner may look at.
type PlanInput struct {
	Observation Observation
	History     History
	// MaxAttempts is boot.attempts from the configuration.
	MaxAttempts int
	// HasAddons is true when at least one Helm release or manifest was configured.
	HasAddons bool
}

// Plan chooses exactly one action from the observation and history. It is a
// pure function: no I/O, no time, so every branch is table-testable.
func Plan(in PlanInput) Action {
	obs, h := in.Observation, in.History

	switch obs.Talos {
	case TalosUnreachable, TalosForeign:
		if h.BootAttempts >= in.MaxAttempts {
			return Action{Kind: ActionFail, Err: ErrAttemptsExhausted}
		}
		return Action{Kind: ActionBootInstaller}

	case TalosMaintenance:
		if !h.ConfigApplied {
			return Action{Kind: ActionApplyConfig}
		}
		if h.BootAttempts >= in.MaxAttempts {
			return Action{Kind: ActionFail, Err: ErrAttemptsExhausted}
		}
		return Action{Kind: ActionRebootToDisk}

	case TalosOurs:
		switch {
		case h.MediaAttached:
			return Action{Kind: ActionDetachMedia}
		case obs.Etcd != EtcdBootstrapped:
			return Action{Kind: ActionBootstrapEtcd}
		case obs.Kubernetes == K8sUnreachable:
			return Action{Kind: ActionWaitKubernetes}
		case !h.AddonsInstalled:
			return Action{Kind: ActionInstallAddons}
		case in.HasAddons && obs.Kubernetes != K8sNodeReady:
			return Action{Kind: ActionWaitReady}
		default:
			return Action{Kind: ActionDone}
		}
	}

	return Action{Kind: ActionFail, Err: ErrUnknownState}
}
