package app

import (
	"fmt"
	"os"

	"github.com/richardsondx/IronLark/internal/agent"
	"github.com/richardsondx/IronLark/internal/checkpoints"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/engine"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/graph"
	"github.com/richardsondx/IronLark/internal/ops"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/state"
	"github.com/richardsondx/IronLark/internal/threads"
)

type App struct {
	Loaded      cfgpkg.Loaded
	Runtime     state.Runtime
	Collector   *ctxpkg.Collector
	Executor    *executor.Executor
	Renderer    render.UI
	Provider    provider.Client
	Agents      agent.Store
	Sessions    sessions.Store
	Threads     threads.Store
	PolicyStore policy.Store
	Patches     patches.Store
	Checkpoints checkpoints.Store
	Graph       *graph.Manager
	Ops         *ops.Manager
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
			RuleURLs:                    runtimeState.Config.Rules.RemoteURLs,
			SemanticMaxFiles:            runtimeState.Config.Tools.SemanticMaxFiles,
			SemanticChunkLines:          runtimeState.Config.Tools.SemanticChunkLines,
			WebSearchResults:            runtimeState.Config.Tools.WebSearchResults,
			DefaultShellTimeoutSec:      runtimeState.Config.Tools.InlineShellTimeoutSec,
			ShellStallWindowSec:         runtimeState.Config.Tools.ShellStallWindowSec,
			AutoBackgroundLongRuns:      runtimeState.Config.Tools.AutoBackgroundLongRuns == nil || *runtimeState.Config.Tools.AutoBackgroundLongRuns,
			LongRunHeuristicsEnabled:    runtimeState.Config.Tools.LongRunHeuristics == nil || *runtimeState.Config.Tools.LongRunHeuristics,
			DurableShellMaxRuntimeSec:   runtimeState.Config.Tools.DurableShellMaxRuntimeSec,
			DurableShellLogPreviewBytes: runtimeState.Config.Tools.DurableShellLogPreviewBytes,
			ProviderModel:               runtimeState.Model,
		},
		Renderer:    renderer,
		Agents:      agent.Store{Dir: loaded.Paths.AgentDir},
		Sessions:    sessions.Store{Dir: loaded.Paths.SessionsDir},
		Threads:     threads.Store{Dir: loaded.Paths.ThreadsDir},
		PolicyStore: policy.Store{Path: loaded.Paths.PolicyPath},
		Patches:     patches.Store{Dir: loaded.Paths.PatchesDir},
		Checkpoints: checkpoints.Store{Dir: loaded.Paths.CheckpointsDir},
		Graph:       graph.NewManager(graph.Store{Dir: loaded.Paths.GraphDir}, runtimeState.Config.Graph, runtimeState.WorkingDir),
		Ops:         ops.NewManager(loaded.Paths),
	}
	app.Executor.OpsFetcher = app.Ops

	if providerCfg, err := runtimeState.ProviderConfig(); err == nil {
		apiKey, keyErr := runtimeState.APIKey()
		if keyErr == nil && providerCfg.Type == "openai-compatible" {
			if runtimeState.ProviderName == "openai" {
				app.Provider = provider.OpenAIResponsesFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
			} else {
				app.Provider = provider.OpenAICompatibleFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
			}
			app.Executor.Provider = app.Provider
		}
	}

	return app, nil
}

func (a *App) Engine() *engine.Engine {
	return &engine.Engine{
		Runtime:     a.Runtime,
		Collector:   a.Collector,
		Executor:    a.Executor,
		Provider:    a.Provider,
		Renderer:    a.Renderer,
		Agents:      a.Agents,
		Sessions:    a.Sessions,
		Threads:     a.Threads,
		PolicyStore: a.PolicyStore,
		Graph:       a.Graph,
		Ops:         a.Ops,
	}
}
