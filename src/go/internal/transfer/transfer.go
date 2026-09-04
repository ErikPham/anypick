package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// EnvelopeJSON is the wire form of one account (version 1, kind
// anypick-account). Field names match the TypeScript envelope exactly so the
// artifacts are interchangeable.
type EnvelopeJSON struct {
	Version int                `json:"version"`
	Kind    string             `json:"kind"`
	Meta    store.Meta         `json:"meta"`
	Proxy   *store.ProxyConfig `json:"proxy"`
	Files   map[string]string  `json:"files"`
}

// BuildEnvelope assembles an envelope in memory for single and package export.
func BuildEnvelope(st *store.AccountStore, ctx context.Context, providerID, name string) (*EnvelopeJSON, error) {
	acct, err := st.Require(providerID, name)
	if err != nil {
		return nil, err
	}
	return &EnvelopeJSON{
		Version: EnvelopeVersion,
		Kind:    EnvelopeKind,
		Meta:    acct.Meta,
		Proxy:   &acct.Proxy,
		Files:   CollectSnapshotFiles(acct.SnapshotDir),
	}, nil
}

// ExportEnvelope serializes a saved account to a portable owner-only JSON
// file via staging + atomic rename.
func ExportEnvelope(st *store.AccountStore, ctx context.Context, providerID, name, outPath string) error {
	payload, err := BuildEnvelope(st, ctx, providerID, name)
	if err != nil {
		return err
	}
	raw, merr := json.MarshalIndent(payload, "", "  ")
	if merr != nil {
		return merr
	}
	// Stage beside the destination rather than in the system temp dir:
	// os.Rename cannot cross a filesystem, so staging in /tmp fails with EXDEV
	// whenever outPath is on another volume (a different drive on Windows, a
	// tmpfs /tmp on Linux).
	if werr := atomicfile.Write(outPath, append(raw, '\n'), 0o600); werr != nil {
		return werr
	}
	fmt.Fprintf(os.Stderr,
		"Warning: exported account %q contains credentials. Store %s safely.\n",
		payload.Meta.Name, outPath)
	return nil
}

// ImportDeps mirrors ImportAccountDeps.
type ImportDeps struct {
	Store     *store.AccountStore
	Providers *provider.Registry
	// ResolveIdentityTarget resolves the final name after identity collision
	// checks; owned by the accounts service because saves use it too.
	ResolveIdentityTarget func(ctx context.Context, providerID, identity, accountName string, allowOverwrite bool) (string, error)
}

// ImportEnvelope decodes, validates, and stages one account envelope under
// providerID. The whole identity-check → commit sequence runs under the
// provider-scoped coordinator lock (invariant #5), so concurrent account
// mutations cannot interleave between collision detection and writeMeta.
// A rejected or failed import leaves DB rows, the active pointer, prior
// snapshots, and live auth untouched.
func ImportEnvelope(deps ImportDeps, ctx context.Context, providerID, name, inPath string, force bool) (store.Meta, error) {
	var zero store.Meta
	var meta store.Meta
	err := lock.WithMutationLock(ctx, deps.Store.Root, refs.ProviderScope(providerID), func(c context.Context) error {
		m, err := importEnvelopeLocked(deps, c, providerID, name, inPath, force)
		meta = m
		return err
	})
	if err != nil {
		return zero, err
	}
	return meta, nil
}

func importEnvelopeLocked(deps ImportDeps, ctx context.Context, providerID, name, inPath string, force bool) (store.Meta, error) {
	var zero store.Meta
	accountName := name
	if _, err := os.Stat(inPath); err != nil {
		return zero, anyerr.Newf(fmt.Sprintf("Import file not found: %s", inPath), "IMPORT_MISSING")
	}
	existing, _ := deps.Store.GetContext(ctx, providerID, accountName)
	if existing != nil && !force {
		return zero, anyerr.Newf(
			fmt.Sprintf("Account %q already exists. Use --force to overwrite.", accountName),
			"ACCOUNT_EXISTS")
	}

	info, err := os.Stat(inPath)
	if err != nil {
		return zero, anyerr.Newf(fmt.Sprintf("Unable to inspect import file: %s", inPath), "IMPORT_MISSING")
	}
	if info.Size() > MaxImportEnvelopeSize {
		return zero, importLimit("Import file exceeds %d bytes.", MaxImportEnvelopeSize)
	}
	raw, rerr := os.ReadFile(inPath)
	if rerr != nil {
		return zero, anyerr.Newf(fmt.Sprintf("Unable to read import file: %s", inPath), "IMPORT_MISSING")
	}
	envelope, derr := DecodeEnvelope(raw, providerID)
	if derr != nil {
		return zero, derr
	}

	pv, perr := deps.Providers.Get(providerID)
	if perr != nil {
		return zero, perr
	}
	identity := envelope.Meta.Identity
	label := envelope.Meta.Label
	if desc, ok := pv.(interface {
		DescribeSnapshot(srcDir string) (provider.SnapshotInfo, error)
	}); ok && len(envelope.Files) > 0 {
		inspectDir, mkErr := os.MkdirTemp("", "anypick-import-inspect-")
		if mkErr != nil {
			return zero, mkErr
		}
		werr := WriteSnapshotFiles(inspectDir, envelope.Files)
		if werr == nil {
			if described, derr2 := desc.DescribeSnapshot(inspectDir); derr2 == nil {
				if described.Identity != "" {
					identity = described.Identity
				}
				if described.Label != "" {
					label = described.Label
				}
			}
		}
		os.RemoveAll(inspectDir)
		if werr != nil {
			return zero, werr
		}
	}

	resolvedName, rerr := deps.ResolveIdentityTarget(ctx, providerID, identity, accountName, force)
	if rerr != nil {
		return zero, rerr
	}
	accountName = resolvedName

	existing, gerr := deps.Store.GetContext(ctx, providerID, accountName)
	if gerr != nil {
		return zero, gerr
	}
	snapshotDir, _, serr := deps.Store.PrepareSnapshot(providerID, accountName)
	if serr != nil {
		return zero, serr
	}
	var meta store.Meta
	var failure error
	func() {
		defer func() { _ = deps.Store.DiscardPreparedSnapshot(snapshotDir) }()
		if werr := WriteSnapshotFiles(snapshotDir, envelope.Files); werr != nil {
			failure = werr
			return
		}
		now := nowISO()
		meta = store.Meta{
			Name:      accountName,
			Provider:  providerID,
			CreatedAt: firstNonEmpty(metaCreatedAt(existing), envelope.Meta.CreatedAt, now),
			UpdatedAt: now,
			Label:     label, Identity: identity,
			Notes:           envelope.Meta.Notes,
			CredentialKind:  envelope.Meta.CredentialKind,
			CredentialClass: envelope.Meta.CredentialClass,
		}
		if werr := deps.Store.WriteMetaContext(ctx, meta, snapshotDir); werr != nil {
			failure = werr
			meta = store.Meta{}
		}
	}()
	if failure != nil {
		return zero, failure
	}

	// Imported proxies are always disabled; options were dropped by the codec.
	if envelope.Proxy != nil {
		cfg := *envelope.Proxy
		cfg.Enabled = false
		cfg.Options = nil
		if serr := deps.Store.SetProxyConfigContext(ctx, providerID, accountName, cfg); serr != nil {
			return zero, serr
		}
	}
	return meta, nil
}

func metaCreatedAt(a *store.Account) string {
	if a == nil {
		return ""
	}
	return a.Meta.CreatedAt
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
