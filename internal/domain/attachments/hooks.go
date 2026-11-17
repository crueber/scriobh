package attachments

import (
	"database/sql"
	"sync"

	"github.com/crueber/scriobh/internal/ops"
)

// ServiceDeleteHooks names the project attachment delete hooks in the op environment.
const ServiceDeleteHooks = "attachments.delete_hooks"

// DeleteHook runs inside the transaction that deletes a project attachment which something still uses (a forced
// delete), before the row goes. The cover layout uses it to return a side whose picture is being deleted to the
// generated cover, so the layout never names a picture that is gone. A hook that fails stops the delete. The function
// it returns, if any, runs after the delete has committed (never in a preview): the cover layout sweeps there the
// pictures it had made from the one that went.
type DeleteHook func(c *ops.Ctx, tx *sql.Tx, projectID, attachmentID string) (after func(), err error)

type deleteHooks struct {
	mu    sync.RWMutex
	hooks []DeleteHook
}

var hooksOnce sync.Mutex

// hooksOf is the environment's hook list, made the first time anything asks for it: AddDeleteHook and Register may run
// in either order, and a hook added before Register must not be lost.
func hooksOf(env *ops.Env) *deleteHooks {
	hooksOnce.Lock()
	defer hooksOnce.Unlock()
	if dh, ok := ops.Service[*deleteHooks](env, ServiceDeleteHooks); ok {
		return dh
	}
	dh := &deleteHooks{}
	env.SetService(ServiceDeleteHooks, dh)
	return dh
}

// AddDeleteHook attaches a hook to the attachment delete, whether or not Register has run yet.
func AddDeleteHook(env *ops.Env, h DeleteHook) {
	dh := hooksOf(env)
	dh.mu.Lock()
	dh.hooks = append(dh.hooks, h)
	dh.mu.Unlock()
}

func runDeleteHooks(c *ops.Ctx, tx *sql.Tx, projectID, attachmentID string) ([]func(), error) {
	dh, ok := ops.Service[*deleteHooks](c.Env, ServiceDeleteHooks)
	if !ok {
		return nil, nil
	}
	dh.mu.RLock()
	hs := append([]DeleteHook(nil), dh.hooks...)
	dh.mu.RUnlock()
	var afters []func()
	for _, h := range hs {
		after, err := h(c, tx, projectID, attachmentID)
		if err != nil {
			return nil, err
		}
		if after != nil {
			afters = append(afters, after)
		}
	}
	return afters, nil
}
