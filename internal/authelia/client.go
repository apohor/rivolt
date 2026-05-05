// Package authelia provides admin-side provisioning of Authelia
// file-backend users via Vault KVv2 and an ExternalSecret refresh.
//
// Wire flow:
//
//	Admin → POST /api/admin/users
//	  rivolt API
//	    ├─ db.CreateUser                                          (rivolt PG)
//	    └─ authelia.Client.UpsertUser
//	         ├─ argon2id hash (in-process)
//	         ├─ Vault GET kv/data/authelia/users                  (read+version)
//	         ├─ merge users_database.yml
//	         ├─ Vault POST kv/data/authelia/users with CAS=v
//	         └─ k8s PATCH externalsecret/authelia-users           (force resync)
//
// The ExternalSecret patch annotation triggers external-secrets to
// re-pull immediately; Authelia's file backend has watch=true, so
// the new user shows up in ~Secret-propagation time (~60s).
//
// All operations are idempotent. UpsertUser overwrites an existing
// entry — this is the password-rotation path. DeleteUser is a no-op
// when the user is missing.
//
// The package is a no-op when not configured (NewFromEnv returns a
// nil client when AUTHELIA_VAULT_ADDR is unset). Callers must check
// Enabled() before relying on it.
package authelia

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"gopkg.in/yaml.v3"
)

// Config holds runtime knobs for the Authelia provisioning client.
//
// Defaults match our in-cluster layout (see rivolt-infra
// apps/external-secrets/resources/manifests/authelia.yaml and
// bootstrap/authelia-add-user.sh) so a typical deployment only
// needs to set VaultAddr + VaultToken.
type Config struct {
	// VaultAddr is the Vault HTTP base URL, e.g.
	// http://vault.vault.svc:8200. When empty, the package is
	// disabled and NewFromEnv returns a nil Client.
	VaultAddr string
	// VaultToken authenticates rivolt to Vault. Loaded from
	// AUTHELIA_VAULT_TOKEN. Issued by the operator with a policy
	// scoped to read+update on `kv/data/authelia/users` only.
	//
	// Optional: when empty the client logs in via the Vault
	// kubernetes auth method using the pod's projected SA token,
	// which is the recommended deployment in-cluster.
	VaultToken string
	// VaultRole is the Vault kubernetes-auth role to log in as
	// when VaultToken is empty. Loaded from AUTHELIA_VAULT_ROLE.
	// The role must be bound to rivolt's ServiceAccount and
	// mapped to a policy that grants read+update on the configured
	// VaultPath.
	VaultRole string
	// VaultKubeAuthPath is the Vault kubernetes-auth mount path,
	// without leading/trailing slashes. Default: "auth/kubernetes".
	VaultKubeAuthPath string
	// VaultPath is the KVv2 path containing the Authelia users
	// blob. Default: "kv/authelia/users".
	VaultPath string
	// DataKey is the field inside the Vault secret holding the
	// raw users_database.yml. Default: "users_database.yml".
	DataKey string
	// KubeNamespace is where the ExternalSecret lives. Default:
	// "authelia". Empty disables the ExternalSecret refresh.
	KubeNamespace string
	// ExternalSecretName is the name of the ExternalSecret to
	// annotate-bump. Default: "authelia-users". Empty disables.
	ExternalSecretName string
	// AdminGroups / UserGroups map rivolt roles to Authelia
	// groups. Default: ["admins"] and ["users"].
	AdminGroups []string
	UserGroups  []string
	// HTTPClient lets tests stub Vault and the k8s API. Default:
	// http.DefaultClient.
	HTTPClient *http.Client
}

// NewFromEnv builds a Client from environment variables. Returns a
// nil Client and nil error when AUTHELIA_VAULT_ADDR is unset, which
// represents the "Authelia integration disabled" case (e.g. local
// dev, self-host without an IdP).
//
//	AUTHELIA_VAULT_ADDR
//	AUTHELIA_VAULT_TOKEN              (optional — when set, used as a static token)
//	AUTHELIA_VAULT_ROLE               (k8s-auth role; required when token is empty)
//	AUTHELIA_VAULT_KUBE_AUTH_PATH     (default "auth/kubernetes")
//	AUTHELIA_VAULT_PATH               (default "kv/authelia/users")
//	AUTHELIA_VAULT_DATA_KEY           (default "users_database.yml")
//	AUTHELIA_KUBE_NAMESPACE           (default "authelia")
//	AUTHELIA_EXTERNAL_SECRET          (default "authelia-users")
//	AUTHELIA_GROUPS_ADMIN             (csv, default "admins")
//	AUTHELIA_GROUPS_USER              (csv, default "users")
func NewFromEnv() (*Client, error) {
	addr := strings.TrimSpace(os.Getenv("AUTHELIA_VAULT_ADDR"))
	if addr == "" {
		return nil, nil
	}
	token := strings.TrimSpace(os.Getenv("AUTHELIA_VAULT_TOKEN"))
	role := strings.TrimSpace(os.Getenv("AUTHELIA_VAULT_ROLE"))
	if token == "" && role == "" {
		return nil, errors.New("AUTHELIA_VAULT_TOKEN or AUTHELIA_VAULT_ROLE must be set when AUTHELIA_VAULT_ADDR is set")
	}
	cfg := Config{
		VaultAddr:          addr,
		VaultToken:         token,
		VaultRole:          role,
		VaultKubeAuthPath:  envOr("AUTHELIA_VAULT_KUBE_AUTH_PATH", "auth/kubernetes"),
		VaultPath:          envOr("AUTHELIA_VAULT_PATH", "kv/authelia/users"),
		DataKey:            envOr("AUTHELIA_VAULT_DATA_KEY", "users_database.yml"),
		KubeNamespace:      envOr("AUTHELIA_KUBE_NAMESPACE", "authelia"),
		ExternalSecretName: envOr("AUTHELIA_EXTERNAL_SECRET", "authelia-users"),
		AdminGroups:        splitCSV(envOr("AUTHELIA_GROUPS_ADMIN", "admins")),
		UserGroups:         splitCSV(envOr("AUTHELIA_GROUPS_USER", "users")),
	}
	return New(cfg)
}

// New builds a Client from explicit Config. Returns an error when
// VaultAddr is empty — callers that want the no-op path should use
// NewFromEnv or check Enabled() before calling.
func New(cfg Config) (*Client, error) {
	if cfg.VaultAddr == "" {
		return nil, errors.New("authelia: VaultAddr is required")
	}
	if cfg.VaultToken == "" && cfg.VaultRole == "" {
		return nil, errors.New("authelia: either VaultToken or VaultRole is required")
	}
	if cfg.VaultKubeAuthPath == "" {
		cfg.VaultKubeAuthPath = "auth/kubernetes"
	}
	if cfg.VaultPath == "" {
		cfg.VaultPath = "kv/authelia/users"
	}
	if cfg.DataKey == "" {
		cfg.DataKey = "users_database.yml"
	}
	if len(cfg.AdminGroups) == 0 {
		cfg.AdminGroups = []string{"admins"}
	}
	if len(cfg.UserGroups) == 0 {
		cfg.UserGroups = []string{"users"}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg}, nil
}

// Client is the Authelia provisioning client. The zero value is
// not usable; build via NewFromEnv or New.
type Client struct {
	cfg Config

	// tokenMu guards cachedToken / cachedTokenExp. Vault tokens
	// minted by the kubernetes auth method are short-lived (the
	// role's TTL, typically 1h) so we cache and refresh ahead of
	// expiry rather than logging in on every call.
	tokenMu        sync.Mutex
	cachedToken    string
	cachedTokenExp time.Time
}

// Enabled reports whether the client is configured. Safe to call
// on a nil receiver — returns false.
func (c *Client) Enabled() bool { return c != nil }

// UpsertUser creates or replaces an Authelia user in the Vault
// users_database.yml, generates a fresh random password, and
// triggers an ExternalSecret refresh. Returns the plaintext
// password — the only time the caller will see it.
//
// Role must be "admin" or "user"; an unknown role maps to user.
// Concurrent calls are serialized via Vault KVv2 CAS with a small
// retry budget; the second writer re-reads and re-merges.
func (c *Client) UpsertUser(ctx context.Context, username, email, displayName, role string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("authelia: client disabled")
	}
	if username == "" {
		return "", errors.New("authelia: username required")
	}
	// Authelia 4.39 rejects users with an empty displayname at
	// startup ("Users.<name>.users: non zero value required"),
	// which crashloops the whole pod. Refuse to write the record
	// rather than poison the YAML.
	if strings.TrimSpace(displayName) == "" {
		return "", errors.New("authelia: display name required")
	}
	pwd, err := generatePassword()
	if err != nil {
		return "", fmt.Errorf("authelia: gen password: %w", err)
	}
	hash, err := hashArgon2id(pwd)
	if err != nil {
		return "", fmt.Errorf("authelia: hash password: %w", err)
	}
	groups := c.cfg.UserGroups
	if role == "admin" {
		groups = c.cfg.AdminGroups
	}
	if err := c.casUpdate(ctx, func(users map[string]any) map[string]any {
		users[username] = map[string]any{
			"displayname": displayName,
			"password":    hash,
			"email":       email,
			"groups":      groups,
		}
		return users
	}); err != nil {
		return "", err
	}
	if err := c.bumpExternalSecret(ctx); err != nil {
		// Don't fail the whole operation — the user is in
		// Vault. Refresh interval (1h) will eventually pick
		// it up; admin can also re-trigger manually. Surface
		// as a warning via the returned error contract: we
		// log here in callers.
		return pwd, fmt.Errorf("authelia: provisioned to vault but force-sync failed: %w", err)
	}
	return pwd, nil
}

// DeleteUser removes a user from the Authelia users_database.yml.
// No-op when the user is absent. Triggers an ExternalSecret
// refresh on success.
func (c *Client) DeleteUser(ctx context.Context, username string) error {
	if !c.Enabled() {
		return errors.New("authelia: client disabled")
	}
	if username == "" {
		return errors.New("authelia: username required")
	}
	changed := false
	if err := c.casUpdate(ctx, func(users map[string]any) map[string]any {
		if _, ok := users[username]; ok {
			delete(users, username)
			changed = true
		}
		return users
	}); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return c.bumpExternalSecret(ctx)
}

// casUpdate reads the current users_database.yml, applies mutate
// to the parsed `users` map, and writes back with KVv2 CAS. Retries
// up to 3 times on version conflict.
func (c *Client) casUpdate(ctx context.Context, mutate func(users map[string]any) map[string]any) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		raw, version, err := c.vaultRead(ctx)
		if err != nil {
			return err
		}
		users := parseUsers(raw)
		users = mutate(users)
		out, err := yaml.Marshal(map[string]any{"users": users})
		if err != nil {
			return fmt.Errorf("authelia: marshal yaml: %w", err)
		}
		if err := c.vaultWriteCAS(ctx, string(out), version); err != nil {
			if errors.Is(err, errCASConflict) {
				lastErr = err
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("authelia: vault CAS conflict after %d attempts: %w", maxAttempts, lastErr)
}

func parseUsers(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var doc struct {
		Users map[string]any `yaml:"users"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil || doc.Users == nil {
		return map[string]any{}
	}
	return doc.Users
}

// errCASConflict is returned by vaultWriteCAS when Vault rejects
// the write because the version we read is no longer current.
var errCASConflict = errors.New("vault CAS conflict")

// vaultToken returns a valid Vault token, logging in via the
// kubernetes auth method when needed. When VaultToken is set
// statically, it's returned verbatim and never refreshed.
func (c *Client) vaultToken(ctx context.Context) (string, error) {
	if c.cfg.VaultToken != "" {
		return c.cfg.VaultToken, nil
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	// Refresh when within 60s of expiry (well before the typical
	// 1h role TTL elapses). time.Time zero is also "expired".
	if c.cachedToken != "" && time.Until(c.cachedTokenExp) > time.Minute {
		return c.cachedToken, nil
	}
	tok, ttl, err := c.vaultK8sLogin(ctx)
	if err != nil {
		return "", err
	}
	c.cachedToken = tok
	c.cachedTokenExp = time.Now().Add(ttl)
	return tok, nil
}

// vaultK8sLogin POSTs the pod's SA JWT to Vault's kubernetes auth
// endpoint and returns (client_token, lease_duration).
func (c *Client) vaultK8sLogin(ctx context.Context) (string, time.Duration, error) {
	if c.cfg.VaultRole == "" {
		return "", 0, errors.New("authelia: VaultRole is required for k8s-auth login")
	}
	const saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	jwt, err := os.ReadFile(saTokenPath)
	if err != nil {
		return "", 0, fmt.Errorf("authelia: read SA token for vault login: %w", err)
	}
	url := strings.TrimRight(c.cfg.VaultAddr, "/") + "/v1/" +
		strings.Trim(c.cfg.VaultKubeAuthPath, "/") + "/login"
	payload := map[string]string{
		"role": c.cfg.VaultRole,
		"jwt":  strings.TrimSpace(string(jwt)),
	}
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("authelia: vault k8s login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("authelia: vault k8s login %d: %s", resp.StatusCode, truncate(body, 256))
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", 0, fmt.Errorf("authelia: vault k8s login decode: %w", err)
	}
	if out.Auth.ClientToken == "" {
		return "", 0, errors.New("authelia: vault k8s login returned empty client_token")
	}
	ttl := time.Duration(out.Auth.LeaseDuration) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return out.Auth.ClientToken, ttl, nil
}

// vaultRead returns (data_key contents, current version) for the
// configured KVv2 secret. A 404 returns ("", 0, nil) so the caller
// can treat "no users yet" as an empty map.
func (c *Client) vaultRead(ctx context.Context) (string, int, error) {
	url := strings.TrimRight(c.cfg.VaultAddr, "/") + "/v1/" + insertDataSegment(c.cfg.VaultPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	tok, err := c.vaultToken(ctx)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("X-Vault-Token", tok)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("authelia: vault read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", 0, nil
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("authelia: vault read %d: %s", resp.StatusCode, truncate(body, 256))
	}
	var out struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", 0, fmt.Errorf("authelia: vault read decode: %w", err)
	}
	v, _ := out.Data.Data[c.cfg.DataKey].(string)
	return v, out.Data.Metadata.Version, nil
}

// vaultWriteCAS writes the data_key contents back to Vault using
// KVv2 compare-and-swap. cas=0 creates the key only if it doesn't
// exist; cas=N requires current version == N.
func (c *Client) vaultWriteCAS(ctx context.Context, raw string, version int) error {
	url := strings.TrimRight(c.cfg.VaultAddr, "/") + "/v1/" + insertDataSegment(c.cfg.VaultPath)
	payload := map[string]any{
		"options": map[string]any{"cas": version},
		"data":    map[string]any{c.cfg.DataKey: raw},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	tok, err := c.vaultToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("authelia: vault write: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	// Vault returns 400 with "check-and-set parameter did not
	// match" on CAS conflict.
	if resp.StatusCode == http.StatusBadRequest && bytes.Contains(body, []byte("check-and-set")) {
		return errCASConflict
	}
	return fmt.Errorf("authelia: vault write %d: %s", resp.StatusCode, truncate(body, 256))
}

// insertDataSegment turns "kv/authelia/users" into
// "kv/data/authelia/users" — the KVv2 read/write API path.
func insertDataSegment(p string) string {
	parts := strings.SplitN(strings.TrimLeft(p, "/"), "/", 2)
	if len(parts) != 2 {
		return p
	}
	return parts[0] + "/data/" + parts[1]
}

// bumpExternalSecret PATCHes the ExternalSecret with a fresh
// force-sync annotation, prompting external-secrets to re-pull
// immediately.
func (c *Client) bumpExternalSecret(ctx context.Context) error {
	if c.cfg.KubeNamespace == "" || c.cfg.ExternalSecretName == "" {
		return nil
	}
	tok, ca, host, err := loadInClusterAuth()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/apis/external-secrets.io/v1/namespaces/%s/externalsecrets/%s",
		host, c.cfg.KubeNamespace, c.cfg.ExternalSecretName)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"force-sync": fmt.Sprintf("%d", time.Now().Unix()),
			},
		},
	}
	buf, _ := json.Marshal(patch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: ca, MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("authelia: patch externalsecret: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("authelia: patch externalsecret %d: %s", resp.StatusCode, truncate(body, 256))
}

// loadInClusterAuth reads the SA token + CA bundle and returns
// (token, ca pool, kube apiserver URL). Errors when not running
// in-cluster.
func loadInClusterAuth() (string, *x509.CertPool, string, error) {
	const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	tok, err := os.ReadFile(filepath.Join(saDir, "token"))
	if err != nil {
		return "", nil, "", fmt.Errorf("authelia: read SA token: %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(saDir, "ca.crt"))
	if err != nil {
		return "", nil, "", fmt.Errorf("authelia: read SA ca.crt: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return "", nil, "", errors.New("authelia: invalid SA ca.crt")
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return "", nil, "", errors.New("authelia: not running in-cluster (no KUBERNETES_SERVICE_HOST)")
	}
	return strings.TrimSpace(string(tok)), pool, fmt.Sprintf("https://%s:%s", host, port), nil
}

// generatePassword returns a 22-char URL-safe random password
// (~131 bits of entropy). Uses base64-no-padding so admins can
// paste it without %-escaping.
func generatePassword() (string, error) {
	const rawBytes = 16
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashArgon2id produces an Authelia-compatible argon2id PHC
// string. Parameters match Authelia 4.x defaults
// (m=64MiB, t=3, p=4, salt=16B, key=32B) so the produced hash
// is interchangeable with `authelia crypto hash generate argon2`.
func hashArgon2id(password string) (string, error) {
	const (
		timeCost    = 3
		memoryKiB   = 64 * 1024
		parallelism = 4
		saltLen     = 16
		keyLen      = 32
	)
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, timeCost, memoryKiB, parallelism, keyLen)
	// PHC: $argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>
	enc := func(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, parallelism, enc(salt), enc(key)), nil
}

// Used by callers / tests that want to verify our hash is parseable
// without pulling in a full PHC library.
var _ = subtle.ConstantTimeCompare

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
