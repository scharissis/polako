package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// providerProbeTimeout bounds `claude auth status`. Probed on 2.1.280 it makes
// no API call, so anything slower than this is a CLI stuck on something else.
const providerProbeTimeout = 5 * time.Second

// claudeProvider names the API provider a shift's runs bill through: firstParty
// reads as anthropic, anything else (bedrock, vertex, foundry — none probed)
// passes through. Best-effort like claudeVersion: a failure, or a CLI without
// `auth status`, leaves it empty. The reply also carries the account's email
// and org; decoding into a one-field struct is what keeps both out of every
// record, log line and terminal.
//
// Not capture: `auth status` exits 1 when no claude.ai login is present, still
// printing the JSON — the usual state on Bedrock, Vertex or an API key, which
// are the providers this field exists to tell apart. So the exit code is
// ignored and the reply alone decides.
func claudeProvider(ctx context.Context, cfg config) string {
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.claudeBin, "auth", "status", "--json")
	cmd.Dir = cfg.dir
	cmd.Env = childEnv(cfg.env)
	out, _ := cmd.Output()
	var status struct {
		APIProvider string `json:"apiProvider"`
	}
	if json.Unmarshal(out, &status) != nil {
		return ""
	}
	if status.APIProvider == "firstParty" {
		return "anthropic"
	}
	return status.APIProvider
}
