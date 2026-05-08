package web

// vault_refs.go — resolve `$vault:<key>` markers in a startReq /
// probeReq before they hit buildRunConfig / handleProbe. The
// runner's RunConfig + the probe's underlying SSH/FTP layer take
// only plaintext credentials; refs are a persistence concern. By
// substituting at this single boundary the rest of the code path
// stays unchanged.

import (
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/vault"
)

// resolveStartReqVaultRefs returns a copy of req with every
// vault-ref-shaped credential field replaced by its plaintext.
// Unresolved refs (key missing from vault) are left intact —
// buildRunConfig / the runner's pool dial will then fail with a
// clear "auth failed" rather than silently dialling with empty
// auth, which is what we want for a deleted secret.
//
// Fields covered:
//   - target_password (single-credential probe / preflight value)
//   - private_key_pem (raw PEM body)
//   - private_key_passphrase
//   - bastion_pass / bastion_passphrase / bastion_private_key_pem
//   - normal_users_csv / large_users_csv / download_users_csv
//     (per-row password column resolved via vault.ResolveCSV)
func resolveStartReqVaultRefs(req startReq, v *vault.Vault) startReq {
	resolveStr := func(s string) string {
		out, _, _ := vault.ResolveString(s, v)
		return out
	}
	req.TargetPassword = resolveStr(req.TargetPassword)
	req.PrivateKeyPEM = resolveStr(req.PrivateKeyPEM)
	req.PrivateKeyPassphrase = resolveStr(req.PrivateKeyPassphrase)
	req.BastionPass = resolveStr(req.BastionPass)
	req.BastionPassphrase = resolveStr(req.BastionPassphrase)
	req.BastionPrivateKeyPEM = resolveStr(req.BastionPrivateKeyPEM)
	req.NormalUsersCSV = vault.ResolveCSV(req.NormalUsersCSV, v)
	req.LargeUsersCSV = vault.ResolveCSV(req.LargeUsersCSV, v)
	req.DownloadUsersCSV = vault.ResolveCSV(req.DownloadUsersCSV, v)
	return req
}
