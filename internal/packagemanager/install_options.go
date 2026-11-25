package packagemanager

import "context"

// InstallOption adjusts how one managed install runs its package manager. With no option an install behaves as Pi's does.
type InstallOption func(*installOptions)

type installOptions struct {
	ignoreScripts bool
	ctx           context.Context
	progress      ProgressCallback
}

// WithIgnoreScripts runs the package manager with `--ignore-scripts`, so no lifecycle script of the installed Package or of its dependencies runs during the install. Npm, pnpm and bun accept the same flag.
//
// pig additive (D194): Piglet resolution installs Packages without running lifecycle scripts unless the user opts in.
func WithIgnoreScripts() InstallOption {
	return func(options *installOptions) { options.ignoreScripts = true }
}

// WithContext ends the install's package manager and git processes when ctx ends, and the install then fails with ctx's error.
//
// dyna patch P9
func WithContext(ctx context.Context) InstallOption {
	return func(options *installOptions) { options.ctx = ctx }
}

// WithProgressReporter receives the progress events a managed Git install raises while it runs, such as the warning for a failed `git clean`. Pi's package manager emits them through the callback set with setProgressCallback.
func WithProgressReporter(progress ProgressCallback) InstallOption {
	return func(options *installOptions) { options.progress = progress }
}

func resolveInstallOptions(opts []InstallOption) installOptions {
	options := installOptions{ctx: context.Background()}
	for _, opt := range opts {
		opt(&options)
	}
	return options
}

// ignoreScriptsArgs is the argument that every supported package manager reads as "run no lifecycle scripts".
func (options installOptions) ignoreScriptsArgs() []string {
	if options.ignoreScripts {
		return []string{"--ignore-scripts"}
	}
	return nil
}
