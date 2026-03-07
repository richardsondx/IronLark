package app

import (
	"fmt"
	"os"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/engine"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/state"
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
	renderer := render.New(os.Stdin, os.Stdout, os.Stderr, runtimeState.JSONOutput, overrides.Color)

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
