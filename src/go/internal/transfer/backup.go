package transfer

// Multi-account backup packages, ported from src/core/account-backup.ts +
// account-restore.ts: export selected accounts as one owner-only package,
// inspect it with dispositions, and restore a selection atomically with
// explicit replacement approval.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

const (
	BackupVersion         = 1
	BackupKind            = "anypick-backup"
	MaxBackupAccounts     = 256
	MaxBackupFiles        = 4096
	MaxBackupDecodedBytes = 128 << 20
	MaxBackupEncodedBytes = 192 << 20
)

// BackupEntry selects one account (provider + name).
type BackupEntry struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type backupPackageAccount struct {
	Provider string        `json:"provider"`
	Name     string        `json:"name"`
	Envelope *EnvelopeJSON `json:"envelope"`
}

type backupPackageJSON struct {
	Version   int                    `json:"version"`
	Kind      string                 `json:"kind"`
	CreatedAt string                 `json:"createdAt"`
	Accounts  []backupPackageAccount `json:"accounts"`
}

func backupError(code, format string, args ...interface{}) error {
	return anyerr.Newf(fmt.Sprintf(format, args...), code, anyerr.WithMutated(false))
}

// ValidateBackupSelection normalizes and orders the selection, rejecting
// empty/duplicate/invalid entries exactly like validateBackupSelection.
func ValidateBackupSelection(accounts []BackupEntry) ([]BackupEntry, error) {
	if len(accounts) == 0 {
		return nil, backupError("BACKUP_EMPTY", "No saved accounts were selected for backup.")
	}
	if len(accounts) > MaxBackupAccounts {
		return nil, backupError("BACKUP_LIMIT",
			"A backup can contain at most %d accounts.", MaxBackupAccounts)
	}
	seen := map[string]bool{}
	selected := make([]BackupEntry, 0, len(accounts))
	for _, a := range accounts {
		if a.Provider == "" {
			return nil, backupError("BACKUP_SELECTION_INVALID", "A selected backup account has no provider.")
		}
		name := slugNormalize(a.Name)
		if name == "" {
			return nil, backupError("BACKUP_SELECTION_INVALID", "A selected backup account has no name.")
		}
		key := a.Provider + "\x00" + name
		if seen[key] {
			return nil, backupError("BACKUP_DUPLICATE", "The backup selection contains a duplicate account.")
		}
		seen[key] = true
		selected = append(selected, BackupEntry{Provider: a.Provider, Name: name})
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Provider != selected[j].Provider {
			return selected[i].Provider < selected[j].Provider
		}
		return selected[i].Name < selected[j].Name
	})
	return selected, nil
}

func slugNormalize(raw string) string { return strings.TrimSpace(strings.ToLower(raw)) }

// ExportSelectedBackup writes one multi-account package. Non-overwrite writes
// fail when the destination exists (no silent clobber).
func ExportSelectedBackup(st *store.AccountStore, ctx context.Context, accounts []BackupEntry, destination string, overwrite bool) (int, error) {
	selected, err := ValidateBackupSelection(accounts)
	if err != nil {
		return 0, err
	}
	pkgAccounts := make([]backupPackageAccount, 0, len(selected))
	fileCount, decodedBytes := 0, 0
	for _, acct := range selected {
		envelope, berr := BuildEnvelope(st, ctx, acct.Provider, acct.Name)
		if berr != nil {
			return 0, berr
		}
		fileCount += len(envelope.Files)
		for _, content := range envelope.Files {
			if raw, derr := stdBase64.DecodeString(content); derr == nil {
				decodedBytes += len(raw)
			}
		}
		if fileCount > MaxBackupFiles || decodedBytes > MaxBackupDecodedBytes {
			return 0, backupError("BACKUP_LIMIT", "Selected accounts exceed the backup package limits.")
		}
		pkgAccounts = append(pkgAccounts, backupPackageAccount{Provider: acct.Provider, Name: acct.Name, Envelope: envelope})
	}
	payload := backupPackageJSON{
		Version: BackupVersion, Kind: BackupKind,
		CreatedAt: nowISO(), Accounts: pkgAccounts,
	}
	raw, merr := json.MarshalIndent(payload, "", "  ")
	if merr != nil {
		return 0, merr
	}
	encoded := append(raw, '\n')
	if len(encoded) > MaxBackupEncodedBytes {
		return 0, backupError("BACKUP_LIMIT", "Backup package exceeds the encoded size limit.")
	}

	if overwrite {
		if werr := atomicfile.Write(destination, encoded, 0o600); werr != nil {
			return 0, werr
		}
		return len(selected), nil
	}
	// Non-overwrite is a different operation, not a rename: a hard link never
	// clobbers an existing destination, so there is no window in which a
	// failed export has already replaced the file. Stage beside the target so
	// the link stays on one filesystem.
	if mkErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkErr != nil {
		return 0, mkErr
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".anypick-backup-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(stage)
	stagedFile := filepath.Join(stage, "package.anypick-backup")
	if werr := os.WriteFile(stagedFile, encoded, 0o600); werr != nil {
		return 0, werr
	}
	if lerr := os.Link(stagedFile, destination); lerr != nil {
		if os.IsExist(lerr) {
			return 0, backupError("BACKUP_DESTINATION_EXISTS", "The backup destination already exists.")
		}
		return 0, lerr
	}
	return len(selected), nil
}

// Disposition mirrors AccountRestoreDisposition.
type Disposition string

const (
	DispositionCreate      Disposition = "create"
	DispositionReplace     Disposition = "replace"
	DispositionBlocked     Disposition = "blocked"
	DispositionUnavailable Disposition = "unavailable"
)

// PreviewEntry mirrors AccountRestorePreviewEntry.
type PreviewEntry struct {
	Provider    string      `json:"provider"`
	Name        string      `json:"name"`
	Label       string      `json:"label"`
	Disposition Disposition `json:"disposition"`
	Reason      string      `json:"reason,omitempty"`
}

// Inspection mirrors AccountRestoreInspection.
type Inspection struct {
	CreatedAt string         `json:"createdAt"`
	Entries   []PreviewEntry `json:"entries"`
}

type plannedEntry struct {
	acct        backupPackageAccount
	label       string
	identity    string
	disposition Disposition
	reason      string
}

// InspectBackup decodes the package and reports per-account dispositions.
func InspectBackup(st *store.AccountStore, reg *provider.Registry, path string) (*Inspection, error) {
	prepared, err := prepareBackup(st, reg, path)
	if err != nil {
		return nil, err
	}
	out := &Inspection{CreatedAt: prepared.createdAt, Entries: make([]PreviewEntry, 0, len(prepared.entries))}
	for _, e := range prepared.entries {
		out.Entries = append(out.Entries, PreviewEntry{
			Provider: e.acct.Provider, Name: e.acct.Name, Label: e.label,
			Disposition: e.disposition, Reason: e.reason,
		})
	}
	return out, nil
}

// RestoreOptions carries explicit replacement approval.
type RestoreOptions struct {
	Replace []BackupEntry
}

// RestoreSelected restores a selection in one atomic batch under sorted,
// provider-scoped coordinator locks (invariant #5). Replacing an existing
// account requires its exact provider/name pair in opts.Replace.
func RestoreSelected(st *store.AccountStore, reg *provider.Registry, path string, selected []BackupEntry, opts RestoreOptions) ([]store.Meta, error) {
	// Sorted provider-scoped locks cover every account the batch touches so a
	// concurrent save/refresh/remove cannot interleave (ADR 0009/0011).
	scopeSet := map[string]bool{}
	for _, e := range append(append([]BackupEntry{}, selected...), opts.Replace...) {
		scopeSet[refs.ProviderScope(e.Provider)] = true
	}
	scopes := make([]string, 0, len(scopeSet))
	for scope := range scopeSet {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var metas []store.Meta
	err := lock.WithMutationLocks(context.Background(), st.Root, scopes,
		func(ctx context.Context) error {
			m, err := restoreSelectedLocked(ctx, st, reg, path, selected, opts)
			metas = m
			return err
		}, 0)
	if err != nil {
		return nil, err
	}
	return metas, nil
}

// restoreSelectedLocked must receive the context that holds the coordinator
// locks: it calls WriteAccountsBatchContext, which re-enters the same scopes.
// Building a fresh context here deadlocks on the in-process path mutex (ADR
// 0009 re-entrancy is carried in the context).
func restoreSelectedLocked(ctx context.Context, st *store.AccountStore, reg *provider.Registry, path string, selected []BackupEntry, opts RestoreOptions) ([]store.Meta, error) {
	prepared, err := prepareBackup(st, reg, path)
	if err != nil {
		return nil, err
	}
	selectedKeys, err := validateRefs(selected)
	if err != nil {
		return nil, err
	}
	replacementKeys, err := validateRefs(opts.Replace)
	if err != nil {
		return nil, err
	}
	for key := range replacementKeys {
		if !selectedKeys[key] {
			return nil, backupError("BACKUP_REPLACE_INVALID",
				"Replacement confirmation does not match the selected accounts.")
		}
	}
	byKey := map[string]plannedEntry{}
	for _, e := range prepared.entries {
		byKey[e.acct.Provider+"\x00"+e.acct.Name] = e
	}
	chosen := make([]plannedEntry, 0, len(selectedKeys))
	for key := range selectedKeys {
		entry, ok := byKey[key]
		if !ok {
			return nil, backupError("BACKUP_SELECTION_INVALID",
				"The restore selection contains an unknown account.")
		}
		switch entry.disposition {
		case DispositionBlocked:
			return nil, backupError("BACKUP_COLLISION",
				"A selected account has a different-name identity collision.")
		case DispositionUnavailable:
			return nil, backupError("BACKUP_UNAVAILABLE",
				"A selected account cannot be prepared by its provider.")
		case DispositionReplace:
			if !replacementKeys[key] {
				return nil, backupError("BACKUP_REPLACE_REQUIRED",
					"Replacing an existing account requires explicit confirmation.")
			}
		}
		chosen = append(chosen, entry)
	}
	sort.Slice(chosen, func(i, j int) bool {
		a, b := chosen[i].acct, chosen[j].acct
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Name < b.Name
	})

	stage, err := os.MkdirTemp("", "anypick-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	now := nowISO()
	writes := make([]store.BatchWrite, 0, len(chosen))
	metas := make([]store.Meta, 0, len(chosen))
	for i, entry := range chosen {
		snapshotDir := filepath.Join(stage, fmt.Sprint(i))
		if werr := WriteSnapshotFiles(snapshotDir, entry.acct.Envelope.Files); werr != nil {
			return nil, werr
		}
		existing, _ := st.GetContext(ctx, entry.acct.Provider, entry.acct.Name)
		createdAt := firstNonEmpty(metaCreatedAt(existing), entry.acct.Envelope.Meta.CreatedAt, now)
		meta := store.Meta{
			Name:            entry.acct.Name,
			Provider:        entry.acct.Provider,
			CreatedAt:       createdAt,
			UpdatedAt:       now,
			Label:           entry.label,
			Identity:        entry.identity,
			Notes:           entry.acct.Envelope.Meta.Notes,
			CredentialKind:  entry.acct.Envelope.Meta.CredentialKind,
			CredentialClass: entry.acct.Envelope.Meta.CredentialClass,
		}
		proxy := store.DefaultProxyConfig()
		if entry.acct.Envelope.Proxy != nil {
			proxy = *entry.acct.Envelope.Proxy
		}
		proxy.Enabled = false
		proxy.Options = nil
		writes = append(writes, store.BatchWrite{Meta: meta, Proxy: proxy, SnapshotDir: snapshotDir})
		metas = append(metas, meta)
	}
	if err := st.WriteAccountsBatchContext(ctx, writes); err != nil {
		return nil, err
	}
	return metas, nil
}

var _ = context.Background

func validateRefs(entries []BackupEntry) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, e := range entries {
		if e.Provider == "" || e.Name == "" {
			return nil, backupError("BACKUP_SELECTION_INVALID",
				"The restore selection contains an invalid account reference.")
		}
		keys[e.Provider+"\x00"+slugNormalize(e.Name)] = true
	}
	return keys, nil
}

type preparedPackage struct {
	createdAt string
	entries   []plannedEntry
}

// readBackupPackage reads and validates the whole package before any state is
// touched. Every envelope is re-run through the same codec as single imports.
func readBackupPackage(path string) (*backupPackageJSON, error) {
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		return nil, backupError("BACKUP_MISSING", "Backup package could not be read.")
	}
	if info.Size() > MaxBackupEncodedBytes {
		return nil, backupError("BACKUP_LIMIT", "Backup package exceeds the encoded size limit.")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, backupError("BACKUP_FORMAT", "Backup package is not valid JSON.")
	}
	var pkg backupPackageJSON
	var probe interface{}
	if json.Unmarshal(raw, &probe) != nil {
		return nil, backupError("BACKUP_FORMAT", "Backup package is not valid JSON.")
	}
	if json.Unmarshal(raw, &pkg) != nil {
		return nil, backupError("BACKUP_FORMAT", "Backup package is not valid JSON.")
	}
	if pkg.Kind != BackupKind || pkg.Version != BackupVersion {
		return nil, backupError("BACKUP_FORMAT", "Unrecognized backup package format.")
	}
	if len(pkg.Accounts) == 0 {
		return nil, backupError("BACKUP_EMPTY", "Backup package contains no accounts.")
	}
	if len(pkg.Accounts) > MaxBackupAccounts {
		return nil, backupError("BACKUP_LIMIT", "A backup can contain at most %d accounts.", MaxBackupAccounts)
	}
	return &pkg, nil
}

// prepareBackup ports prepareAccountBackup: describe each envelope against its
// provider, then compute dispositions from local identities.
func prepareBackup(st *store.AccountStore, reg *provider.Registry, path string) (*preparedPackage, error) {
	pkg, err := readBackupPackage(path)
	if err != nil {
		return nil, err
	}
	type described struct {
		acct        backupPackageAccount
		label       string
		identity    string
		unavailable bool
	}
	list := make([]described, 0, len(pkg.Accounts))
	stage, merr := os.MkdirTemp("", "anypick-inspect-")
	if merr != nil {
		return nil, merr
	}
	defer os.RemoveAll(stage)

	for i, acct := range pkg.Accounts {
		envRaw, jerr := json.Marshal(acct.Envelope)
		if jerr != nil {
			return nil, jerr
		}
		env, derr := DecodeEnvelope(envRaw, acct.Envelope.Meta.Provider)
		if derr != nil {
			return nil, backupError("BACKUP_FORMAT", "Backup entry %d failed validation: %v", i+1, derr)
		}
		item := described{
			acct: backupPackageAccount{Provider: env.Meta.Provider, Name: acct.Name, Envelope: &EnvelopeJSON{
				Version: env.Version, Kind: env.Kind, Meta: env.Meta, Proxy: env.Proxy, Files: env.Files,
			}},
			label:    firstNonEmpty(env.Meta.Label, acct.Name),
			identity: env.Meta.Identity,
		}
		pv, gerr := reg.Get(env.Meta.Provider)
		if gerr != nil {
			item.unavailable = true
			list = append(list, item)
			continue
		}
		if desc, ok := pv.(interface {
			DescribeSnapshot(srcDir string) (provider.SnapshotInfo, error)
		}); ok && len(env.Files) > 0 {
			dir := filepath.Join(stage, fmt.Sprint(i))
			werr := WriteSnapshotFiles(dir, env.Files)
			if werr != nil {
				item.unavailable = true
			} else if details, derr2 := desc.DescribeSnapshot(dir); derr2 == nil {
				if details.Label != "" {
					item.label = details.Label
				}
				if details.Identity != "" {
					item.identity = details.Identity
				}
			} else {
				item.unavailable = true
			}
		}
		list = append(list, item)
	}

	// Duplicate identities inside the package itself are conflicts too.
	packageIdentityCounts := map[string]int{}
	for _, item := range list {
		if item.identity != "" {
			packageIdentityCounts[item.acct.Provider+"\x00"+normalizeIdentityKey(item.identity)]++
		}
	}

	entries := make([]plannedEntry, 0, len(list))
	for _, item := range list {
		e := plannedEntry{acct: item.acct, label: item.label, identity: item.identity}
		if item.unavailable {
			e.disposition = DispositionUnavailable
			e.reason = "provider-unavailable"
			entries = append(entries, e)
			continue
		}
		local, lerr := st.ListAccounts(item.acct.Provider)
		if lerr != nil {
			return nil, lerr
		}
		normIdentity := normalizeIdentityKey(item.identity)
		collision := false
		if normIdentity != "" {
			for _, acct := range local {
				if acct.Meta.Name != item.acct.Name && acct.Meta.Identity != "" &&
					normalizeIdentityKey(acct.Meta.Identity) == normIdentity {
					collision = true
					break
				}
			}
			if packageIdentityCounts[item.acct.Provider+"\x00"+normIdentity] > 1 {
				collision = true
			}
		}
		if collision {
			e.disposition = DispositionBlocked
			e.reason = "identity-conflict"
			entries = append(entries, e)
			continue
		}
		exists := false
		for _, acct := range local {
			if acct.Meta.Name == item.acct.Name {
				exists = true
				break
			}
		}
		if exists {
			e.disposition = DispositionReplace
		} else {
			e.disposition = DispositionCreate
		}
		entries = append(entries, e)
	}
	return &preparedPackage{createdAt: pkg.CreatedAt, entries: entries}, nil
}

// normalizeIdentityKey mirrors the case-insensitive email comparison used for
// identity collisions.
func normalizeIdentityKey(identity string) string {
	if identity == "" {
		return ""
	}
	var parsed struct {
		Email string `json:"email"`
	}
	if json.Unmarshal([]byte(identity), &parsed) == nil && parsed.Email != "" {
		return strings.ToLower(parsed.Email)
	}
	return strings.ToLower(identity)
}
