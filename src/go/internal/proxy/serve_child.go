package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// ServeExisting is the detached-child entry: it adopts the state the parent
// persisted (token, port, instance id), serves until SIGTERM/SIGINT, then
// cleans its own records. The instance echo on /health is what lets the
// parent — or any later invocation — prove this process is theirs (PROC-01).
func (s *Service) ServeExisting(ctx context.Context, providerID, accountName string) error {
	state, err := s.Store.ReadProxyState(providerID, accountName)
	if err != nil {
		return err
	}
	if state == nil || state.Endpoint == "" || state.Token == "" || state.InstanceID == "" {
		return fmt.Errorf("no proxy state to serve for %s/%s", providerID, accountName)
	}
	upstream, err := s.Resolver.ResolveUpstream(ctx, providerID, accountName)
	if err != nil {
		return err
	}

	server := s.buildServer(providerID, accountName, state.InstanceID, state.Token, upstream)
	port := portOf(state.Endpoint)
	if err := server.Listen("127.0.0.1", port); err != nil {
		return err
	}

	proxyLogf("%s/%s listening on %s", providerID, accountName,
		strings.TrimPrefix(state.Endpoint, "http://"))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sigCh:
	case err := <-server.Err():
		if err != nil && err != http.ErrServerClosed {
			_ = server.Close()
			return errors.Join(err, s.cleanupStoppedInstance(ctx, providerID, accountName, state.InstanceID))
		}
	}
	_ = server.Close()
	return s.cleanupStoppedInstance(ctx, providerID, accountName, state.InstanceID)
}

func (s *Service) cleanupStoppedInstance(ctx context.Context, providerID, accountName, instanceID string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*healthTimeout)
	defer cancel()
	return lock.WithMutationLocks(cleanupCtx, s.Store.Root, []string{
		refs.ProviderScope(providerID), proxyScope(providerID, accountName),
	}, func(c context.Context) error {
		leaseErr := s.cleanupLeasesContext(c, providerID, accountName, instanceID)
		state, stateErr := s.Store.ReadProxyStateContext(c, providerID, accountName)
		if stateErr != nil {
			return errors.Join(leaseErr, stateErr)
		}
		if state == nil || state.InstanceID != instanceID {
			return leaseErr
		}
		return errors.Join(
			leaseErr,
			s.Store.ClearProxyStateContext(c, providerID, accountName),
		)
	}, 0)
}

var (
	_ = store.ProxyRuntimeState{}
	_ = syscall.SIGTERM
)
