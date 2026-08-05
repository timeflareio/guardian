package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

// Network selection is the first thing `config init` asks, because it is the
// question with the widest consequences and the one an operator arrives already
// knowing the answer to.
//
// It exists because the three values it writes are only meaningful together — an
// endpoint pair belonging to one chain id — and typing them by hand invites the
// one mistake that costs bond: a wrong chain id leaves queries working and every
// transaction failing, so it looks like a healthy guardian right up to the point
// where it has missed a reveal window.

// networkChoice is what selection resolved to, ready for applyInitSettings. An
// empty id means nothing was selected and the compiled defaults stand.
type networkChoice struct {
	id           string
	chainID      string
	rpcEndpoint  string
	grpcEndpoint string
	grpcTLS      bool
}

// selected reports whether anything was chosen.
func (c networkChoice) selected() bool {
	return c.id != ""
}

// choiceFrom converts a published entry into what gets written. grpc_tls follows
// from the entry's locality, which the chain's own verify-networks ties to the
// endpoint schemes, so it is the rule rather than an inference from it.
func choiceFrom(n config.RegistryNetwork) networkChoice {
	return networkChoice{
		id:           n.ID,
		chainID:      n.ChainID,
		rpcEndpoint:  n.RPCEndpoint(),
		grpcEndpoint: n.GRPCEndpoint(),
		grpcTLS:      n.GRPCTLS(),
	}
}

// collectNetwork resolves the network from the published registry, a flag, or a
// prompt.
//
// Failure to read the list is fatal to an unattended run and survivable to an
// interactive one. The asymmetry is deliberate: with nobody there to answer, "no
// network named" means "the published default", so silently substituting the
// compiled devnet literals would hand back precisely the real-looking wrong
// value the selection exists to prevent. A setup command that fails loudly can
// be run again.
func collectNetwork(u *ui.Printer, flags initFlags, interactive bool) (networkChoice, error) {
	// An operator who says "custom" has chosen to configure the endpoints
	// themselves, so there is nothing to read and no reason to require a network.
	// It is also the only way an unattended setup completes with no route out to
	// the published list.
	if flags.network == config.CustomNetworkID {
		return networkChoice{id: config.CustomNetworkID}, nil
	}

	source := config.NetworkListSource()
	list, err := config.FetchNetworkList(context.Background(), source)
	if err != nil {
		switch {
		case flags.network != "":
			return networkChoice{}, errors.Wrapf(err,
				"cannot resolve --network %s", flags.network)
		case !interactive:
			return networkChoice{}, errors.Wrap(err,
				"cannot resolve the default network (pass --network custom to configure the endpoints yourself)")
		}
		u.Warning("Could not read the published network list: %v", err)
		u.Note(ui.Indent1 + "Falling back to entering the network by hand.\n")
		return promptNetworkByHand(u)
	}

	if flags.network != "" {
		return namedNetwork(list, flags.network)
	}
	if !interactive {
		// Validate guaranteed the default resolves.
		chosen, _ := list.DefaultNetwork()
		if reason := chosen.Unusable(); reason != "" {
			return networkChoice{}, errors.Errorf(
				"the default network %q %s", chosen.ID, reason)
		}
		return choiceFrom(chosen), nil
	}
	return promptNetwork(u, list)
}

// namedNetwork resolves --network, naming what was on offer when it does not
// resolve. A script pointed at a network that no longer exists has to fail
// rather than quietly land somewhere else.
func namedNetwork(list *config.NetworkList, id string) (networkChoice, error) {
	chosen, ok := list.Find(id)
	if !ok {
		return networkChoice{}, errors.Errorf("no network %q is published (available: %s, or %s)",
			id, strings.Join(list.IDs(), ", "), config.CustomNetworkID)
	}
	if reason := chosen.Unusable(); reason != "" {
		return networkChoice{}, errors.Errorf("network %q %s", id, reason)
	}
	return choiceFrom(chosen), nil
}

// promptNetwork offers the published networks, plus the custom escape hatch that
// is this daemon's own and never the chain's — a private node, or a network the
// list does not carry, has to remain configurable.
func promptNetwork(u *ui.Printer, list *config.NetworkList) (networkChoice, error) {
	u.Step("🌐 Step 1: Network")
	u.TextLn(ui.Indent1 + "A guardian serves one network. Choosing here sets the chain id and both")
	u.TextLn(ui.Indent1 + "endpoints together, which is the point: a wrong chain id leaves queries")
	u.TextLn(ui.Indent1 + "working and every transaction failing.\n")

	// Only usable networks are numbered. An unusable one is still shown, with the
	// reason: an operator who cannot find the network they were told to join
	// needs to know it was seen and rejected, not that it was never there.
	selectable := make([]config.RegistryNetwork, 0, len(list.Networks))
	for _, n := range list.Networks {
		if reason := n.Unusable(); reason != "" {
			u.Text(ui.Indent1 + "-. ")
			u.Value("%s (%s)", n.Label, n.ID)
			u.TextLn(" — unavailable: %s", reason)
			continue
		}
		selectable = append(selectable, n)
		u.Text(ui.Indent1)
		// The id as well as the label, because it is what --network takes when the
		// same setup is scripted next time.
		u.Value("%d. %s (%s)", len(selectable), n.Label, n.ID)
		u.Text(" — ")
		u.Key("%s", n.ChainID)
		if n.ID == list.Default {
			u.TextLn("  (default)")
		} else {
			u.EmptyLine()
		}
		u.TextLn(ui.Indent2 + "rpc  " + n.RPCEndpoint())
		u.Text(ui.Indent2 + "grpc " + n.GRPCEndpoint())
		if n.GRPCTLS() {
			u.TextLn("  (TLS)")
		} else {
			u.EmptyLine()
		}
	}

	custom := len(selectable) + 1
	u.Text(ui.Indent1)
	u.Value("%d. Custom", custom)
	u.TextLn(" — enter the chain id and endpoints by hand")
	u.EmptyLine()

	defaultIndex := 1
	for i, n := range selectable {
		if n.ID == list.Default {
			defaultIndex = i + 1
		}
	}

	answer := strings.TrimSpace(u.PromptInput(ui.Indent1 + "🔀 Select a network [" + strconv.Itoa(defaultIndex) + "]: "))
	if answer == "" {
		answer = strconv.Itoa(defaultIndex)
	}
	pick, err := strconv.Atoi(answer)
	if err != nil || pick < 1 || pick > custom {
		return networkChoice{}, errors.Errorf("select a number between 1 and %d", custom)
	}
	if pick == custom {
		return promptNetworkByHand(u)
	}

	chosen := selectable[pick-1]
	u.Success("Network: %s (%s)", chosen.Label, chosen.ChainID)
	u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")
	return choiceFrom(chosen), nil
}

// promptNetworkByHand takes the three values directly. Empty keeps the compiled
// default, so an operator can override one field without restating the others.
//
// grpc_tls is deliberately not inferred here: nothing in a typed host:port says
// whether it terminates TLS, and guessing from the shape of a hostname is how a
// remote endpoint ends up dialled in clear.
func promptNetworkByHand(u *ui.Printer) (networkChoice, error) {
	defaults := config.DefaultConfig()
	u.EmptyLine()
	u.Note(ui.Indent1 + "Leave a value empty to keep the default shown.")

	chainID := promptWithDefault(u, "chain id", defaults.ChainID)
	rpc := promptWithDefault(u, "CometBFT RPC endpoint", defaults.RPCEndpoint)
	grpcEndpoint := promptWithDefault(u, "gRPC endpoint", defaults.GRPCEndpoint)

	u.Note(ui.Indent1 + "Set grpc_tls if that gRPC endpoint is TLS-terminated:")
	u.Text(ui.Indent2)
	u.Command("guardianctl config set grpc-tls true\n")
	u.Note(ui.Indent1 + "─────────────────────────────────────────────────\n")

	return networkChoice{
		id:           config.CustomNetworkID,
		chainID:      chainID,
		rpcEndpoint:  rpc,
		grpcEndpoint: grpcEndpoint,
	}, nil
}

func promptWithDefault(u *ui.Printer, label, fallback string) string {
	answer := strings.TrimSpace(u.PromptInput(ui.Indent1 + label + " [" + fallback + "]: "))
	if answer == "" {
		return fallback
	}
	return answer
}
