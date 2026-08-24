package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	SecretPolicyRequireReferences = "require-references"
	SecretPolicyAllowPlaintext    = "allow-plaintext"
	secretReferencePrefix         = "secret://"
	secretFileMaxSize             = 64 << 10
	configMigrationMaxSize        = 16 << 20
)

// MigrationResult describes a completed plaintext-to-reference conversion.
type MigrationResult struct {
	Count int
	Names []string
}

// StoreCredential provisions one named credential without exposing it in argv or JSON.
// The platform writer is create-only: rotation uses a new name so an accidental command
// cannot overwrite the credential currently referenced by a production configuration.
func StoreCredential(secretDir, name, value string) error {
	if strings.TrimSpace(secretDir) == "" {
		return errors.New("credential directory is required")
	}
	if !validSecretName(name) {
		return fmt.Errorf("invalid credential name %q", name)
	}
	if err := validateCredentialValue(value); err != nil {
		return err
	}
	if err := ensureCredentialDirectory(secretDir); err != nil {
		return err
	}
	return writeCredentialFile(secretDir, name, value)
}

func validateCredentialValue(value string) error {
	if value == "" {
		return errors.New("credential is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("credential contains NUL")
	}
	// Unix credential files include one trailing newline. Keep the complete file
	// within the same cap enforced by readCredentialFile on every platform.
	if len(value) > secretFileMaxSize-1 {
		return errors.New("credential exceeds 64 KiB")
	}
	return nil
}

func validateSecretPolicy(policy string) error {
	switch policy {
	case SecretPolicyRequireReferences, SecretPolicyAllowPlaintext:
		return nil
	default:
		return fmt.Errorf("secret_policy must be %q or %q", SecretPolicyRequireReferences, SecretPolicyAllowPlaintext)
	}
}

func credentialDirectory() string {
	// This helper is used only for writes. CREDENTIALS_DIRECTORY is systemd's
	// private read-only LoadCredential mount and SERVERDESK_CREDENTIALS_DIRECTORY
	// is a legacy read source; neither is ever a managed write destination.
	return strings.TrimSpace(os.Getenv("SERVERDESK_CREDENTIALS_STORE"))
}

// managedCredentialDirectory is the daemon-writable, service-owned credential
// store used for secrets entered through the authenticated management UI. It is
// intentionally separate from systemd's CREDENTIALS_DIRECTORY, which is a
// read-only tmpfs populated by LoadCredential. Without an explicit override the
// store lives beside config.local.json (for packaged installs this is
// /var/lib/serverdesk/credentials on Linux and C:\serverdesk\credentials on
// Windows).
func managedCredentialDirectory(configPath string) string {
	if dir := strings.TrimSpace(os.Getenv("SERVERDESK_CREDENTIALS_STORE")); dir != "" {
		return filepath.Clean(dir)
	}
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Clean(configPath)), "credentials")
}

func credentialReadDirectories(managedDir string) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	// A reference written by the service must resolve from the same service-owned
	// store after restart. Explicit SERVERDESK_* comes next, while systemd's
	// ephemeral LoadCredential directory remains a read-only compatibility source.
	add(managedDir)
	add(os.Getenv("SERVERDESK_CREDENTIALS_STORE"))
	add(os.Getenv("SERVERDESK_CREDENTIALS_DIRECTORY"))
	add(os.Getenv("CREDENTIALS_DIRECTORY"))
	return dirs
}

func validSecretName(name string) bool {
	if name == "" || len(name) > 128 || name[0] == '.' {
		return false
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func isManagedSecretName(name string) bool {
	if !strings.HasPrefix(name, "serverdesk.managed.") {
		return false
	}
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || len(name)-dot-1 != 16 {
		return false
	}
	_, err := hex.DecodeString(name[dot+1:])
	return err == nil
}

func collectSecretReferences(value any, out map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			collectSecretReferences(child, out)
		}
	case []any:
		for _, child := range typed {
			collectSecretReferences(child, out)
		}
	case string:
		if name, referenced, err := secretReferenceName(typed); err == nil && referenced {
			out[name] = true
		}
	}
}

func documentSecretReferences(doc map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return out
	}
	var generic any
	if json.Unmarshal(encoded, &generic) == nil {
		collectSecretReferences(generic, out)
	}
	return out
}

func removeManagedCredentialNames(dir string, names []string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	for _, name := range names {
		if isManagedSecretName(name) {
			_ = removeCredentialFile(dir, name)
		}
	}
}

// cleanupManagedCredentialStore keeps both the current document and its
// immediately previous (.bak) generation resolvable. Older service-generated
// versions are removed only after the new config has been fsynced and renamed.
// Externally provisioned names are never considered for deletion.
func cleanupManagedCredentialStore(dir string, current, rollback map[string]json.RawMessage) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	keep := documentSecretReferences(current)
	for name := range documentSecretReferences(rollback) {
		keep[name] = true
	}
	names, err := listCredentialNames(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, name := range names {
		if !isManagedSecretName(name) || keep[name] {
			continue
		}
		if err := removeCredentialFile(dir, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func secretReferenceName(value string) (string, bool, error) {
	if !strings.HasPrefix(value, secretReferencePrefix) {
		return "", false, nil
	}
	name := strings.TrimPrefix(value, secretReferencePrefix)
	if !validSecretName(name) {
		return "", true, fmt.Errorf("invalid secret reference name %q", name)
	}
	return name, true, nil
}

func resolveSecretValueFrom(value, policy, field, managedDir string) (string, error) {
	if value == "" {
		return "", nil
	}
	name, referenced, err := secretReferenceName(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	if !referenced {
		if policy == SecretPolicyRequireReferences {
			return "", fmt.Errorf("%s contains plaintext; use secret://NAME or run -migrate-secrets", field)
		}
		return value, nil
	}
	dirs := credentialReadDirectories(managedDir)
	if len(dirs) == 0 {
		return "", fmt.Errorf("%s uses %s%s but no CREDENTIALS_DIRECTORY-compatible source is configured (set writable SERVERDESK_CREDENTIALS_STORE, legacy SERVERDESK_CREDENTIALS_DIRECTORY, or systemd CREDENTIALS_DIRECTORY)", field, secretReferencePrefix, name)
	}
	var lastErr error
	for _, dir := range dirs {
		secret, err := readCredentialFile(dir, name)
		if err != nil {
			lastErr = err
			continue
		}
		if secret == "" {
			return "", fmt.Errorf("%s (%s%s): credential is empty", field, secretReferencePrefix, name)
		}
		return secret, nil
	}
	return "", fmt.Errorf("%s (%s%s): %w", field, secretReferencePrefix, name, lastErr)
}

func resolveSecretValue(value, policy, field string) (string, error) {
	return resolveSecretValueFrom(value, policy, field, "")
}

func resolveConfigSecrets(c *Config, managedDir string) error {
	resolve := func(dst *string, field string) error {
		value, err := resolveSecretValueFrom(*dst, c.SecretPolicy, field, managedDir)
		if err != nil {
			return err
		}
		*dst = value
		return nil
	}
	if err := resolve(&c.SNMPCommunity, "snmp_community"); err != nil {
		return err
	}
	if err := resolve(&c.Notifications.WebhookURL, "notifications.webhook_url"); err != nil {
		return err
	}
	if c.Trap.Community != nil {
		if err := resolve(c.Trap.Community, "trap.community"); err != nil {
			return err
		}
	}
	for i := range c.Clusters {
		cluster := &c.Clusters[i]
		prefix := fmt.Sprintf("clusters[%d]", i)
		for _, target := range []struct {
			value *string
			name  string
		}{
			{&cluster.AdminPassword, prefix + ".admin_password"},
			{&cluster.NodeRootPassword, prefix + ".node_root_password"},
			{&cluster.SNMPCommunity, prefix + ".snmp_community"},
		} {
			if err := resolve(target.value, target.name); err != nil {
				return err
			}
		}
		for j := range cluster.Nodes {
			if err := resolve(&cluster.Nodes[j].RootPassword, fmt.Sprintf("%s.nodes[%d].root_password", prefix, j)); err != nil {
				return err
			}
		}
	}
	for i := range c.EdgeDevices {
		device := &c.EdgeDevices[i]
		prefix := fmt.Sprintf("edge_devices[%d]", i)
		for _, target := range []struct {
			value *string
			name  string
		}{
			{&device.Community, prefix + ".community"},
			{&device.WebPassword, prefix + ".web_password"},
			{&device.Password, prefix + ".password"},
			{&device.BmcPassword, prefix + ".bmc_password"},
		} {
			if err := resolve(target.value, target.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResolveEdgeDeviceSecretReferences resolves secret:// fields for a device being
// hot-added at runtime. Plaintext values are intentionally left unchanged here;
// Store.AddEntry separately converts them to references before persistence.
func ResolveEdgeDeviceSecretReferences(device *EdgeDevice) error {
	return ResolveEdgeDeviceSecretReferencesFrom(device, "")
}

// ResolveEdgeDeviceSecretReferencesFrom resolves explicit references for a
// hot-added device against the same managed store used by Store.AddEntry.
func ResolveEdgeDeviceSecretReferencesFrom(device *EdgeDevice, managedDir string) error {
	if device == nil {
		return errors.New("edge device is nil")
	}
	prefix := "edge_devices[" + device.Key + "]"
	for _, target := range []struct {
		value *string
		name  string
	}{
		{&device.Community, prefix + ".community"},
		{&device.WebPassword, prefix + ".web_password"},
		{&device.Password, prefix + ".password"},
		{&device.BmcPassword, prefix + ".bmc_password"},
	} {
		value, err := resolveSecretValueFrom(*target.value, SecretPolicyAllowPlaintext, target.name, managedDir)
		if err != nil {
			return err
		}
		*target.value = value
	}
	return nil
}

// ResolveNotificationWebhookReference resolves an explicitly supplied
// secret:// webhook value for a live settings update. Plaintext remains in
// memory only; Store persists it as a versioned managed reference.
func ResolveNotificationWebhookReference(value, managedDir string) (string, error) {
	return resolveSecretValueFrom(value, SecretPolicyAllowPlaintext, "notifications.webhook_url", managedDir)
}

var protectedSecretKeys = map[string]bool{
	"password": true, "passwd": true, "root_password": true,
	"admin_password": true, "node_root_password": true, "web_password": true,
	"bmc_password": true, "community": true, "snmp_community": true,
	"trap_community": true, "secret": true, "token": true,
	"api_key": true, "private_key": true,
	"webhook_url": true,
}

func pathIdentity(value any, index int) string {
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"key", "name", "id"} {
			if id, ok := object[key].(string); ok && id != "" {
				return id
			}
		}
	}
	return fmt.Sprintf("item-%d", index)
}

func secretNameForPath(path []string) (string, error) {
	joined := strings.Join(path, ".")
	var b strings.Builder
	b.WriteString("serverdesk.managed.")
	lastDot := false
	for _, r := range strings.ToLower(joined) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
			lastDot = false
		} else if !lastDot {
			b.WriteByte('.')
			lastDot = true
		}
	}
	base := strings.Trim(b.String(), ".")
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate credential version: %w", err)
	}
	suffix := "." + hex.EncodeToString(random[:])
	if len(base)+len(suffix) > 128 {
		base = base[:128-len(suffix)]
	}
	return base + suffix, nil
}

func protectDocumentSecrets(value any, path []string, secretDir string, result *MigrationResult) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			// _comment/_*_example are documentation metadata, never live credentials.
			if strings.HasPrefix(key, "_") {
				continue
			}
			childPath := append(append([]string{}, path...), key)
			if protectedSecretKeys[strings.ToLower(key)] {
				plain, ok := typed[key].(string)
				if !ok || plain == "" {
					continue
				}
				if _, referenced, err := secretReferenceName(plain); err != nil {
					return fmt.Errorf("%s: %w", strings.Join(childPath, "."), err)
				} else if referenced {
					continue
				}
				name, err := secretNameForPath(childPath)
				if err != nil {
					return err
				}
				if strings.TrimSpace(secretDir) == "" {
					return fmt.Errorf("%s contains plaintext but no writable SERVERDESK_CREDENTIALS_STORE is set", strings.Join(childPath, "."))
				}
				if err := ensureCredentialDirectory(secretDir); err != nil {
					return fmt.Errorf("prepare managed credential store: %w", err)
				}
				if err := writeCredentialFile(secretDir, name, plain); err != nil {
					return fmt.Errorf("write credential %s: %w", name, err)
				}
				typed[key] = secretReferencePrefix + name
				result.Count++
				result.Names = append(result.Names, name)
				continue
			}
			if err := protectDocumentSecrets(typed[key], childPath, secretDir, result); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			childPath := append(append([]string{}, path...), pathIdentity(child, i))
			if err := protectDocumentSecrets(child, childPath, secretDir, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func protectRequiredRawDocument(doc map[string]json.RawMessage) error {
	_, err := protectRequiredRawDocumentAt(doc, credentialDirectory())
	return err
}

func protectRequiredRawDocumentAt(doc map[string]json.RawMessage, managedDir string) (MigrationResult, error) {
	var result MigrationResult
	var policy string
	if raw, ok := doc["secret_policy"]; ok {
		_ = json.Unmarshal(raw, &policy)
	}
	if policy == "" {
		policy = SecretPolicyRequireReferences
	}
	if err := validateSecretPolicy(policy); err != nil {
		return result, err
	}
	if policy != SecretPolicyRequireReferences {
		return result, nil
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic map[string]any
	if err := decoder.Decode(&generic); err != nil {
		return result, err
	}
	if err := protectDocumentSecrets(generic, nil, managedDir, &result); err != nil {
		return result, err
	}
	if result.Count == 0 {
		return result, nil
	}
	protected, err := json.Marshal(generic)
	if err != nil {
		return result, err
	}
	var replacement map[string]json.RawMessage
	if err := json.Unmarshal(protected, &replacement); err != nil {
		return result, err
	}
	for key := range doc {
		delete(doc, key)
	}
	for key, value := range replacement {
		doc[key] = value
	}
	return result, nil
}

// MigratePlaintextSecrets atomically replaces plaintext secret fields with secret:// references.
// Credential files are created first and never overwritten with a different value, so a failed
// run is safe to retry. On Linux the target can feed systemd LoadCredential; on Windows the
// platform writer stores machine-scoped DPAPI ciphertext.
func MigratePlaintextSecrets(configPath, secretDir string) (MigrationResult, error) {
	var result MigrationResult
	if strings.TrimSpace(secretDir) == "" {
		return result, errors.New("credential directory is required")
	}
	cleanPath := filepath.Clean(configPath)
	originalInfo, err := os.Lstat(cleanPath)
	if err != nil {
		return result, fmt.Errorf("inspect config: %w", err)
	}
	if !originalInfo.Mode().IsRegular() || originalInfo.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("config must be a regular non-symlink file")
	}
	original, err := os.Open(cleanPath)
	if err != nil {
		return result, fmt.Errorf("open config: %w", err)
	}
	openedInfo, err := original.Stat()
	if err != nil {
		_ = original.Close()
		return result, fmt.Errorf("stat config: %w", err)
	}
	if !os.SameFile(originalInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		_ = original.Close()
		return result, errors.New("config changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(original, configMigrationMaxSize+1))
	closeErr := original.Close()
	if err != nil {
		return result, fmt.Errorf("read config: %w", err)
	}
	if closeErr != nil {
		return result, fmt.Errorf("close config: %w", closeErr)
	}
	if len(data) > configMigrationMaxSize {
		return result, errors.New("read config: file exceeds 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return result, fmt.Errorf("decode config: %w", err)
	}
	if doc == nil {
		return result, errors.New("decode config: top level must be an object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return result, errors.New("decode config: trailing data")
	}
	if err := ensureCredentialDirectory(secretDir); err != nil {
		return result, err
	}
	if err := protectDocumentSecrets(doc, nil, secretDir, &result); err != nil {
		return result, err
	}
	doc["secret_policy"] = SecretPolicyRequireReferences
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return result, fmt.Errorf("encode config: %w", err)
	}
	out = append(out, '\n')
	backup := cleanPath + ".pre-secrets.bak"
	if err := writeExclusiveFile0600(backup, data); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return result, fmt.Errorf("write pre-migration backup: %w", err)
		}
		info, inspectErr := os.Lstat(backup)
		if inspectErr != nil {
			return result, fmt.Errorf("inspect pre-migration backup: %w", inspectErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return result, errors.New("pre-migration backup must be a regular non-symlink file")
		}
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(cleanPath), "."+filepath.Base(cleanPath)+".secrets-*")
	if err != nil {
		return result, fmt.Errorf("create migrated config: %w", err)
	}
	tmp := tmpFile.Name()
	committed := false
	defer func() {
		_ = tmpFile.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(0o600); err != nil {
		return result, fmt.Errorf("secure migrated config: %w", err)
	}
	if _, err := tmpFile.Write(out); err != nil {
		return result, fmt.Errorf("write migrated config: %w", err)
	}
	if err := applyReplacementMetadata(tmpFile, originalInfo); err != nil {
		return result, fmt.Errorf("preserve migrated config metadata: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return result, fmt.Errorf("sync migrated config: %w", err)
	}
	tmpInfo, err := tmpFile.Stat()
	if err != nil {
		return result, fmt.Errorf("stat migrated config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return result, fmt.Errorf("close migrated config: %w", err)
	}
	pathInfo, err := os.Lstat(tmp)
	if err != nil || !os.SameFile(tmpInfo, pathInfo) || !pathInfo.Mode().IsRegular() {
		return result, errors.New("migrated config temporary file changed before replace")
	}
	if err := replaceConfigFile(tmp, cleanPath); err != nil {
		return result, fmt.Errorf("replace migrated config: %w", err)
	}
	committed = true
	if err := syncParentDirectory(filepath.Dir(cleanPath)); err != nil {
		return result, fmt.Errorf("sync migrated config directory: %w", err)
	}
	sort.Strings(result.Names)
	return result, nil
}

func writeExclusiveFile0600(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
