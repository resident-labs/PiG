package coding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/internal/codingagent/compaction"
)

// The paths a model transfer takes.
const (
	// TransferUnchanged: the model stayed and the conversation fits.
	TransferUnchanged = "unchanged"
	// TransferSwitched: the conversation moved whole, because it fits the new model.
	TransferSwitched = "switched"
	// TransferCompacted: a summary replaced the older part of the conversation.
	TransferCompacted = "compacted"
	// TransferBoundary: it did not fit and no summary did, so the new model was told to read the session file.
	TransferBoundary = "boundary"
	// TransferAborted: the transfer was cancelled, and the model did not change.
	TransferAborted = "aborted"
)

// transferSummaryTimeout bounds both summary attempts together. A test shortens it.
var transferSummaryTimeout = 10 * time.Minute

// transferCompactAbove is the context size above which a transfer to another model compacts on the old one even when
// the conversation fits: the new model starts with a cold prompt cache, and the old one still has its cache.
const transferCompactAbove = 100_000

// ModelTransferOptions are the target's settings besides its model.
type ModelTransferOptions struct {
	ThinkingLevel ai.ModelThinkingLevel
	// CompactionTriggerTokens is the target's trigger (SetCompactionTriggerTokens); zero clears it.
	CompactionTriggerTokens int
}

// ModelTransferResult is how TransferModel ended.
type ModelTransferResult struct {
	Path         string
	TokensBefore int
	TokensAfter  int
	// Reason says which model summarized, or why the transfer could not use a summary.
	Reason string
}

// transferCompaction holds the source and optional fallback for one compaction, and its outcome.
type transferCompaction struct {
	source, fallback *ai.Model
	sourceErr        error // set before compaction if the recorded source could not be found
	settings         compaction.CompactionSettings
	summarizedBy     *ai.Model
	compacted        bool
	aborted          bool
	err              error
}

// TransferModel moves the conversation to target between runs. A conversation that fits and is small switches at once.
// Otherwise it is compacted first, on the model that last held it, because that model can read all of it and still has
// its prompt cache. If the old model fails, the target gets one compaction attempt within the same time budget.
// If no summary fits, the session switches anyway: a conversation that fits stays whole, and one that does not
// leaves target a boundary that names the session file. The session file keeps every entry on all paths.
// Abort cancels the compaction and leaves the model unchanged.
//
// dyna patch P4 (PATCHES.md): Pi checks the context against a new model only at its next request, when it may already
// be too large for it, and compacts on the new model with a cold cache. A host that moves sessions between models needs
// the move checked and done on the old one first.
func (s *Session) TransferModel(ctx context.Context, target *ai.Model, options ModelTransferOptions) (ModelTransferResult, error) {
	if err := s.CheckModelAuth(target); err != nil {
		return ModelTransferResult{}, err
	}
	if s.IsStreaming() {
		return ModelTransferResult{}, errAgentAlreadyProcessing
	}
	// A process that restarted on target still has the conversation of the model the branch records.
	source := s.Model()
	var sourceErr error
	if recorded := GetBranchSelection(s.currentBranch(), s.modelRuntime.GetModel); recorded != nil && !recorded.Unresolvable &&
		recorded.Provider != "" && recorded.ModelID != "" &&
		(source == nil || providerID(source) != recorded.Provider || source.ID != recorded.ModelID) {
		if model := s.modelRuntime.GetModel(recorded.Provider, recorded.ModelID); model != nil {
			source = model
		} else if source == nil || ai.ModelsAreEqual(source, target) {
			// After a restart the old model might no longer be registered. A running session still owns its model.
			source = nil
			sourceErr = fmt.Errorf("old model %s/%s is no longer registered", recorded.Provider, recorded.ModelID)
		}
	}
	if source == nil && sourceErr == nil {
		sourceErr = errors.New("no old model selected")
	}
	configured, err := s.SettingsManager().GetCompactionSettings(target)
	if err != nil {
		return ModelTransferResult{}, err
	}
	settings := compaction.CompactionSettings{Enabled: true, ReserveTokens: configured.ReserveTokens, KeepRecentTokens: configured.KeepRecentTokens, TriggerTokens: options.CompactionTriggerTokens}
	contextTokens := func() int {
		return compaction.EstimateProjectedContextTokens(s.inner.BuildSessionProjectionView(), s.currentBranch()).Tokens
	}
	// A virtual model's limits are checked when a request is routed, against the model it reaches.
	exceeds := func(limits compaction.CompactionSettings) bool {
		return !IsVirtualModel(target) && target.Capabilities.ContextWindow > 0 && compaction.ShouldCompact(contextTokens(), target.Capabilities.ContextWindow, limits)
	}
	changesModel := !ai.ModelsAreEqual(source, target)
	apply := func() error {
		s.SetCompactionTriggerTokens(options.CompactionTriggerTokens)
		if changesModel || !ai.ModelsAreEqual(s.Model(), target) {
			if err := s.SetModel(target); err != nil {
				return err
			}
		}
		return s.SetThinkingLevel(options.ThinkingLevel)
	}
	unchanged := TransferUnchanged
	if changesModel {
		unchanged = TransferSwitched
	}

	before := contextTokens()
	if ctx.Err() != nil {
		return ModelTransferResult{Path: TransferAborted, TokensBefore: before, TokensAfter: before}, nil
	}
	if !exceeds(settings) && !(changesModel && before > transferCompactAbove) {
		return ModelTransferResult{Path: unchanged, TokensBefore: before, TokensAfter: before}, apply()
	}

	hardLimit := target.Capabilities.ContextWindow - settings.ReserveTokens
	limit := hardLimit
	if settings.TriggerTokens > 0 {
		limit = min(limit, settings.TriggerTokens)
	}
	run := &transferCompaction{source: source, sourceErr: sourceErr, settings: settings}
	if changesModel {
		run.fallback = target
	}
	run.settings.KeepRecentTokens = max(1, min(settings.KeepRecentTokens, limit/2))
	summaryCtx, cancel := context.WithTimeout(ctx, transferSummaryTimeout)
	defer cancel()
	_, err = s.runAutoCompactionFor(summaryCtx, "threshold", false, run)
	if err != nil {
		return ModelTransferResult{}, err
	}
	timedOut := errors.Is(summaryCtx.Err(), context.DeadlineExceeded)
	if ctx.Err() != nil || run.aborted {
		return ModelTransferResult{Path: TransferAborted, TokensBefore: before, TokensAfter: contextTokens()}, nil
	}
	// Only the hard limit forces a boundary: over the trigger alone, the conversation still works.
	whole := settings
	whole.TriggerTokens = 0
	fits := !exceeds(whole)
	var reason string
	switch {
	case run.compacted && !fits:
		reason = "the summary left too much context"
		if model := run.summarizedBy; model != nil {
			reason = fmt.Sprintf("the summary by %s/%s left too much context", providerID(model), model.ID)
		}
	case run.compacted:
		if model := run.summarizedBy; model != nil {
			reason = fmt.Sprintf("summarized by %s/%s", providerID(model), model.ID)
		}
	case timedOut:
		reason = "the summary timed out"
	case run.err != nil:
		reason = run.err.Error()
	default:
		reason = "there was nothing to summarize"
	}
	if err := apply(); err != nil {
		return ModelTransferResult{}, err
	}
	if fits {
		path := unchanged
		if run.compacted {
			path = TransferCompacted
		}
		return ModelTransferResult{Path: path, TokensBefore: before, TokensAfter: contextTokens(), Reason: reason}, nil
	}
	if err := s.appendTransferBoundary(reason, run.settings, limit); err != nil {
		return ModelTransferResult{}, err
	}
	return ModelTransferResult{Path: TransferBoundary, TokensBefore: before, TokensAfter: contextTokens(), Reason: reason}, nil
}

// summarizeTransfer keeps both attempts inside one compaction so an abort cancels either model and stops the fallback.
func (s *Session) summarizeTransfer(ctx context.Context, prep compaction.CompactionPreparation, reason string, run *transferCompaction) (compaction.CompactionResult, error) {
	var result compaction.CompactionResult
	err := run.sourceErr
	if run.source != nil {
		result, err = s.runDefaultCompaction(ctx, prep, run.source, "", reason)
		if err == nil {
			run.summarizedBy = run.source
			return result, nil
		}
	}
	if run.fallback == nil || ctx.Err() != nil {
		return result, err
	}
	// Providers can report an earlier timeout as text in an error response, independently of our overall deadline.
	deadline, _ := ctx.Deadline()
	message := strings.ToLower(err.Error())
	if run.source != nil && time.Until(deadline) <= transferSummaryTimeout/2 &&
		(errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "deadline exceeded") || strings.Contains(message, "etimedout")) {
		return result, err
	}
	result, fallbackErr := s.runDefaultCompaction(ctx, prep, run.fallback, "", reason)
	if fallbackErr != nil {
		if run.source == nil {
			return result, fmt.Errorf("%v; new model: %w", err, fallbackErr)
		}
		return result, fmt.Errorf("old model: %v; new model: %w", err, fallbackErr)
	}
	run.summarizedBy = run.fallback
	return result, nil
}

// appendTransferBoundary compacts without a model. The summary tells the new model where the session file is, and the
// tail kept is what the cut point keeps within limit. Older entries stay in the file.
func (s *Session) appendTransferBoundary(reason string, settings compaction.CompactionSettings, limit int) error {
	branch := s.currentBranch()
	projection := s.inner.BuildSessionProjectionView()
	before := compaction.EstimateProjectedContextTokens(projection, branch).Tokens
	prep := compaction.PrepareCompaction(branch, settings)
	var readFiles, modifiedFiles []string
	if prep != nil {
		readFiles, modifiedFiles = compaction.ComputeFileLists(prep.FileOps)
	}
	var previous string
	if latest := icodingagent.GetLatestCompactionEntry(branch); latest != nil && len(latest.Summary)/4 <= settings.KeepRecentTokens {
		previous = latest.Summary
	}
	if runes := []rune(reason); len(runes) > 300 {
		reason = string(runes[:300])
	}
	summary := transferBoundary(s.inner.Path(), reason, previous) + compaction.FormatFileOperations(readFiles, modifiedFiles)

	// One oversized tool result can pass the limit alone, and a cut cannot split it. Keep no tail then.
	firstKept := ""
	if prep != nil {
		tail, kept := 0, false
		for _, entry := range projection.Entries {
			kept = kept || entry.SourceEntry.Base().ID == prep.FirstKeptEntryID
			if kept {
				for _, message := range entry.Messages {
					tail += compaction.EstimateTokens(message)
				}
			}
		}
		if kept && tail+len(summary)/4 <= limit {
			firstKept = prep.FirstKeptEntryID
		}
	}
	details := compaction.CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles}

	s.emitCompactionEvent(agent.CompactionStartEvent{Reason: "threshold"})
	entryID, err := s.appendCompaction(s.inner, summary, firstKept, before, details, false, nil)
	if err != nil {
		s.emitOrderedEventSync(agent.CompactionEndEvent{Reason: "threshold", ErrorMessage: err.Error()})
		return err
	}
	s.refreshContext()
	if entry, ok := s.inner.GetEntry(entryID); ok {
		s.emitSessionCompact(s.backgroundContext(), entry, false, "threshold", false)
	}
	s.emitCompactionEvent(agent.CompactionEndEvent{
		Reason:               "threshold",
		Summary:              summary,
		FirstKeptEntryID:     firstKept,
		TokensBefore:         before,
		EstimatedTokensAfter: estimateMessagesTokens(s.agent.Messages()),
		Details:              details,
	})
	return nil
}

// transferBoundary is the summary of a boundary: why the conversation was left behind, and where it is.
func transferBoundary(sessionFile, reason, previousSummary string) string {
	where := "The earlier conversation is not available. Continue the work from what follows."
	if sessionFile != "" {
		where = fmt.Sprintf("The whole earlier conversation is in the previous session's file, %s. It is JSONL, one entry per line, oldest first. Read it for the context you need, then continue the work.", sessionFile)
	}
	earlier := ""
	if previousSummary != "" {
		earlier = "\n\nThe latest summary of that conversation:\n\n" + previousSummary
	}
	return fmt.Sprintf("This session moved to a new model without its earlier conversation: %s. %s%s", reason, where, earlier)
}
