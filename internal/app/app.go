package app

import (
	"fmt"
	"os"

	"github.com/richardson/lark/internal/checkpoints"
	cfgpkg "github.com/richardson/lark/internal/config"
	ctxpkg "github.com/richardson/lark/internal/context"
	"github.com/richardson/lark/internal/engine"
	"github.com/richardson/lark/internal/executor"
	"github.com/richardson/lark/internal/patches"
	"github.com/richardson/lark/internal/policy"
	"github.com/richardson/lark/internal/provider"
	"github.com/richardson/lark/internal/redact"
	"github.com/richardson/lark/internal/render"
	"github.com/richardson/lark/internal/search"
	"github.com/richardson/lark/internal/sessions"
	"github.com/richardson/lark/internal/state"
)

type App struct {
	Loaded      cfgpkg.Loaded
	Runtime     state.Runtime
	Collector   *ctxpkg.Collector
	Executor    *executor.Executor
	Renderer    *render.Renderer
	Provider    provider.Client
	Sessions    sessions.Store
	Patches     patches.Store
	Checkpoints checkpoints.Store
}

func New(overrides state.Overrides) (*App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	loaded, err := cfgpkg.Load(cwd)
	if err != nil {
		return nil, err
	}
	if err := cfgpkg.EnsureDirs(loaded.Paths); err != nil {
		return nil, err
	}

	runtimeState, err := state.Resolve(loaded, overrides)
	if err != nil {
		return nil, err
	}

	redactor := redact.New(runtimeState.Config.Security.RedactPatterns)
	classifier := policy.NewClassifier(runtimeState.Config.Security.ProtectedPaths)
	renderer := render.New(os.Stdin, os.Stdout, os.Stderr, runtimeState.JSONOutput)

	app := &App{
		Loaded:    loaded,
		Runtime:   runtimeState,
		Collector: ctxpkg.New(redactor),
		Executor: &executor.Executor{
			WorkingDir:     runtimeState.WorkingDir,
			MaxOutputBytes: runtimeState.Config.Context.MaxCommandOutputBytes,
			MaxListEntries: runtimeState.Config.Context.MaxListEntries,
			MaxFileBytes:   runtimeState.Config.Context.MaxFileBytes,
			Redactor:       redactor,
			Classifier:     classifier,
			PatchStore: patches.Store{
				Dir: loaded.Paths.PatchesDir,
			},
			CheckpointStore: checkpoints.Store{
				Dir: loaded.Paths.CheckpointsDir,
			},
			Searcher: search.Searcher{
				UserAgent: "lark-term/1.0",
			},
			RuleURLs:           runtimeState.Config.Rules.RemoteURLs,
			SemanticMaxFiles:   runtimeState.Config.Tools.SemanticMaxFiles,
			SemanticChunkLines: runtimeState.Config.Tools.SemanticChunkLines,
			WebSearchResults:   runtimeState.Config.Tools.WebSearchResults,
		},
		Renderer:    renderer,
		Sessions:    sessions.Store{Dir: loaded.Paths.SessionsDir},
		Patches:     patches.Store{Dir: loaded.Paths.PatchesDir},
		Checkpoints: checkpoints.Store{Dir: loaded.Paths.CheckpointsDir},
	}

	if providerCfg, err := runtimeState.ProviderConfig(); err == nil {
		apiKey, keyErr := runtimeState.APIKey()
		if keyErr == nil && providerCfg.Type == "openai-compatible" {
			app.Provider = provider.OpenAICompatibleFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
		}
	}

	return app, nil
}

func (a *App) Engine() *engine.Engine {
	return &engine.Engine{
		Runtime:   a.Runtime,
		Collector: a.Collector,
		Executor:  a.Executor,
		Provider:  a.Provider,
		Renderer:  a.Renderer,
		Sessions:  a.Sessions,
	}
}
