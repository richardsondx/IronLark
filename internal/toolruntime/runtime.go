package toolruntime

import (
	"context"
	"fmt"
	"sync"

	"github.com/richardsondx/IronLark/internal/core"
)

// Handler is a typed execution unit for a single action kind.
type Handler interface {
	ActionType() core.ActionType
	Name() string
	Execute(ctx context.Context, action core.Action, readOnly bool, onChunk func(core.ActionOutputChunk)) (core.ActionResult, error)
}

type Runtime struct {
	mu       sync.RWMutex
	handlers map[core.ActionType]Handler
}

func New() *Runtime {
	return &Runtime{
		handlers: map[core.ActionType]Handler{},
	}
}

func (r *Runtime) Register(handler Handler) error {
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	actionType := handler.ActionType()
	if actionType == "" {
		return fmt.Errorf("handler %T has empty action type", handler)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[actionType]; exists {
		return fmt.Errorf("handler already registered for %s", actionType)
	}
	r.handlers[actionType] = handler
	return nil
}

func (r *Runtime) Handles(actionType core.ActionType) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[actionType]
	return ok
}

func (r *Runtime) HandlerName(actionType core.ActionType) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[actionType]
	if !ok {
		return ""
	}
	return handler.Name()
}

func (r *Runtime) Execute(ctx context.Context, action core.Action, readOnly bool, onChunk func(core.ActionOutputChunk)) (core.ActionResult, error) {
	if r == nil {
		return core.ActionResult{}, fmt.Errorf("tool runtime is not configured")
	}
	r.mu.RLock()
	handler, ok := r.handlers[action.Type]
	r.mu.RUnlock()
	if !ok {
		return core.ActionResult{}, fmt.Errorf("no handler registered for %s", action.Type)
	}
	return handler.Execute(ctx, action, readOnly, onChunk)
}
